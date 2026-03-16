//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// uinput ioctl codes (x86-64 Linux).
const (
	uiSetEvBit  uintptr = 0x40045564
	uiSetKeyBit uintptr = 0x40045565
	uiSetRelBit uintptr = 0x40045566
	uiDevCreate uintptr = 0x5501
	uiDevDestroy uintptr = 0x5502

	evSyn     uint16 = 0x00
	evKey     uint16 = 0x01
	evRel     uint16 = 0x02
	relX      uint16 = 0x00
	relY      uint16 = 0x01
	synReport uint16 = 0x00
	btnLeft   uint16 = 0x110
	busUSB    uint16 = 0x03

	uinputNameLen = 80
	absCnt        = 64
)

// uinputUserDev is the legacy uinput device descriptor (1116 bytes).
type uiInputID struct{ Bustype, Vendor, Product, Version uint16 }
type uinputUserDev struct {
	Name         [uinputNameLen]byte
	ID           uiInputID
	FfEffectsMax uint32
	Absmax       [absCnt]int32
	Absmin       [absCnt]int32
	Absfuzz      [absCnt]int32
	Absflat      [absCnt]int32
}

// warpEvent mirrors struct input_event on 64-bit Linux (24 bytes).
type warpEvent struct {
	Sec, Usec  int64
	Type, Code uint16
	Value      int32
}

// warpMouseToCenter attempts to move the OS cursor to (w/2, h/2) and returns
// the virtual starting position (vx, vy) that matches where the cursor ended up.
// If only a corner slam is possible it returns (0, 0).
func warpMouseToCenter(w, h int) (vx, vy int) {
	display, xauth := findDisplayEnv()

	// --- try xdotool ---
	xdotool := findBin("xdotool")
	if xdotool != "" && display != "" {
		env := []string{"DISPLAY=" + display}
		if xauth != "" {
			env = append(env, "XAUTHORITY="+xauth)
		}
		cmd := exec.Command(xdotool, "mousemove", strconv.Itoa(w/2), strconv.Itoa(h/2))
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			log.Printf("xdotool mousemove: %v", err)
		} else {
			log.Printf("cursor warped to centre (%d,%d) via xdotool", w/2, h/2)
			return w / 2, h / 2
		}
	} else {
		log.Printf("xdotool unavailable (binary=%q display=%q)", xdotool, display)
	}

	// --- fall back to uinput slam-to-corner ---
	if err := slamToCorner(); err != nil {
		log.Printf("uinput corner slam: %v — move cursor to centre manually", err)
		return w / 2, h / 2 // unknown position; guess centre
	}
	log.Printf("cursor slammed to top-left corner via uinput; tracking from (0,0)")
	return 0, 0
}

// findDisplayEnv scans /proc to find DISPLAY and XAUTHORITY from any process
// owned by the real user (works when running under sudo).
func findDisplayEnv() (display, xauth string) {
	// env vars set in current process first
	display = os.Getenv("DISPLAY")
	xauth = os.Getenv("XAUTHORITY")
	if display != "" {
		return
	}

	// find the real user's UID
	username := os.Getenv("SUDO_USER")
	if username == "" {
		username = os.Getenv("USER")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return
	}

	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := os.Lstat("/proc/" + e.Name())
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || strconv.Itoa(int(st.Uid)) != u.Uid {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(string(data), "\x00") {
			if display == "" && strings.HasPrefix(kv, "DISPLAY=") {
				display = strings.TrimPrefix(kv, "DISPLAY=")
			}
			if xauth == "" && strings.HasPrefix(kv, "XAUTHORITY=") {
				xauth = strings.TrimPrefix(kv, "XAUTHORITY=")
			}
		}
		if display != "" && xauth != "" {
			break
		}
	}
	return
}

// readCursorPos returns the actual OS cursor position using xdotool.
// Returns (x, y, true) on success, (0, 0, false) if xdotool is unavailable.
func readCursorPos(w, h int) (int, int, bool) {
	xdotool := findBin("xdotool")
	if xdotool == "" {
		return 0, 0, false
	}
	display, xauth := findDisplayEnv()
	if display == "" {
		return 0, 0, false
	}
	env := []string{"DISPLAY=" + display}
	if xauth != "" {
		env = append(env, "XAUTHORITY="+xauth)
	}
	cmd := exec.Command(xdotool, "getmouselocation")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, false
	}
	var x, y int
	if n, _ := fmt.Sscanf(string(out), "x:%d y:%d", &x, &y); n != 2 {
		return 0, 0, false
	}
	return clamp(x, 0, w-1), clamp(y, 0, h-1), true
}

// findBin looks for a binary in PATH and common NixOS system locations.
func findBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range []string{
		"/run/current-system/sw/bin/" + name,
		"/nix/var/nix/profiles/default/bin/" + name,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// slamToCorner creates a temporary uinput mouse and sends enough large negative
// deltas to guarantee the cursor is clamped at (0, 0).
func slamToCorner() error {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /dev/uinput: %w", err)
	}
	defer func() {
		syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uiDevDestroy, 0)
		f.Close()
	}()

	ioc := func(req, arg uintptr) error {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg)
		if errno != 0 {
			return errno
		}
		return nil
	}

	// A minimal mouse: EV_REL + BTN_LEFT (so libinput classifies it as pointer).
	if err := ioc(uiSetEvBit, uintptr(evRel)); err != nil {
		return fmt.Errorf("SET_EVBIT EV_REL: %w", err)
	}
	if err := ioc(uiSetRelBit, uintptr(relX)); err != nil {
		return fmt.Errorf("SET_RELBIT REL_X: %w", err)
	}
	if err := ioc(uiSetRelBit, uintptr(relY)); err != nil {
		return fmt.Errorf("SET_RELBIT REL_Y: %w", err)
	}
	if err := ioc(uiSetEvBit, uintptr(evKey)); err != nil {
		return fmt.Errorf("SET_EVBIT EV_KEY: %w", err)
	}
	if err := ioc(uiSetKeyBit, uintptr(btnLeft)); err != nil {
		return fmt.Errorf("SET_KEYBIT BTN_LEFT: %w", err)
	}

	var dev uinputUserDev
	copy(dev.Name[:], "kvm-warp")
	dev.ID.Bustype = busUSB
	b := (*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))
	if _, err := f.Write(b[:]); err != nil {
		return fmt.Errorf("write uinput_user_dev: %w", err)
	}
	if err := ioc(uiDevCreate, 0); err != nil {
		return fmt.Errorf("DEV_CREATE: %w", err)
	}

	time.Sleep(100 * time.Millisecond)

	send := func(typ, code uint16, val int32) {
		ev := warpEvent{Type: typ, Code: code, Value: val}
		b := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))
		f.Write(b[:])
	}

	// Eight slams of -32767 guarantees we hit the corner regardless of speed.
	for range 8 {
		send(evRel, relX, -32767)
		send(evRel, relY, -32767)
		send(evSyn, synReport, 0)
	}
	time.Sleep(80 * time.Millisecond)
	return nil
}
