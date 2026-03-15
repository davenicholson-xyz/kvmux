//go:build linux

// Package evdev reads raw relative mouse events from a Linux evdev device.
// This works correctly under both X11 and Wayland.
package evdev

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	evSyn     = 0
	evRel     = 2
	relX      = 0
	relY      = 1
	synReport = 0
)

// inputEvent mirrors struct input_event from linux/input.h (64-bit layout).
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// Delta is a synchronised relative movement from one input_event batch.
type Delta struct{ DX, DY int }

// Reader reads events from an evdev device.
type Reader struct {
	f      *os.File
	device string
}

// Open opens the given evdev device. Pass an empty string to auto-detect.
func Open(device string) (*Reader, error) {
	if device == "" {
		var err error
		device, err = findMouseDevice()
		if err != nil {
			return nil, fmt.Errorf("auto-detect mouse: %w", err)
		}
	}
	f, err := os.Open(device)
	if err != nil {
		return nil, err
	}
	return &Reader{f: f, device: device}, nil
}

func (r *Reader) Device() string { return r.device }
func (r *Reader) Close()         { r.f.Close() }

// eviocgrab = _IOW('E', 0x90, int) — exclusively grab/release the device.
// While grabbed, the OS does not process the events (cursor won't move on server).
const eviocgrab = 0x40044590

func (r *Reader) Grab() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, r.f.Fd(), eviocgrab, 1); errno != 0 {
		return errno
	}
	return nil
}

func (r *Reader) Ungrab() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, r.f.Fd(), eviocgrab, 0); errno != 0 {
		return errno
	}
	return nil
}

// ReadEvents blocks, reading events and sending synced deltas to ch.
// Returns on read error (e.g. device closed).
func (r *Reader) ReadEvents(ch chan<- Delta) error {
	log.Printf("evdev: reading from %s (struct size %d bytes)", r.device, binary.Size(inputEvent{}))
	var ev inputEvent
	var dx, dy int
	first := true
	for {
		if err := binary.Read(r.f, binary.NativeEndian, &ev); err != nil {
			return err
		}
		if first {
			log.Printf("evdev: first event type=%d code=%d value=%d", ev.Type, ev.Code, ev.Value)
			first = false
		}
		switch ev.Type {
		case evRel:
			switch ev.Code {
			case relX:
				dx += int(ev.Value)
			case relY:
				dy += int(ev.Value)
			}
		case evSyn:
			if ev.Code == synReport {
				if dx != 0 || dy != 0 {
					ch <- Delta{DX: dx, DY: dy}
				}
				dx, dy = 0, 0
			}
		}
	}
}

// findMouseDevice returns the most appropriate mouse evdev path.
//
// Strategy (in order):
//  1. /dev/input/by-id/*-event-mouse  — stable USB ID symlinks, best choice
//  2. /dev/input/by-path/*-event-mouse — stable path-based symlinks
//  3. /proc/bus/input/devices scan    — fallback, picks device with EV_REL +
//     REL_X + REL_Y that the kernel also registered as a mouseN node, and
//     whose name does not contain "keyboard".
func findMouseDevice() (string, error) {
	// 1. by-id
	if dev, err := globFirst("/dev/input/by-id/*-event-mouse"); err == nil {
		return dev, nil
	}
	// 2. by-path
	if dev, err := globFirst("/dev/input/by-path/*-event-mouse"); err == nil {
		return dev, nil
	}
	// 3. /proc scan
	return findMouseFromProc()
}

func globFirst(pattern string) (string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("no match for %s", pattern)
	}
	return matches[0], nil
}

func findMouseFromProc() (string, error) {
	data, err := os.ReadFile("/proc/bus/input/devices")
	if err != nil {
		return "", err
	}

	type candidate struct {
		event      string
		hasMouse   bool
		hasKeyboard bool
	}

	var (
		evFlags  uint64
		relFlags uint64
		handlers []string
		name     string
		best     *candidate
	)

	flush := func() {
		defer func() { evFlags = 0; relFlags = 0; handlers = nil; name = "" }()
		if evFlags&(1<<evRel) == 0 {
			return
		}
		if relFlags&(1<<relX) == 0 || relFlags&(1<<relY) == 0 {
			return
		}
		var eventNode string
		hasMouse, hasKeyboard := false, false
		for _, h := range handlers {
			if strings.HasPrefix(h, "event") {
				eventNode = "/dev/input/" + h
			}
			if strings.HasPrefix(h, "mouse") {
				hasMouse = true
			}
			if strings.HasPrefix(h, "kbd") {
				hasKeyboard = true
			}
		}
		nameLower := strings.ToLower(name)
		if strings.Contains(nameLower, "keyboard") {
			hasKeyboard = true
		}
		if eventNode == "" {
			return
		}
		c := &candidate{event: eventNode, hasMouse: hasMouse, hasKeyboard: hasKeyboard}
		if best == nil {
			best = c
			return
		}
		// Prefer: has mouse node, no keyboard association.
		score := func(x *candidate) int {
			s := 0
			if x.hasMouse {
				s += 2
			}
			if !x.hasKeyboard {
				s++
			}
			return s
		}
		if score(c) > score(best) {
			best = c
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "N: Name=") {
			name = strings.Trim(strings.TrimPrefix(line, "N: Name="), "\"")
		}
		if strings.HasPrefix(line, "B: EV=") {
			fmt.Sscanf(strings.TrimPrefix(line, "B: EV="), "%x", &evFlags)
		}
		if strings.HasPrefix(line, "B: REL=") {
			fmt.Sscanf(strings.TrimPrefix(line, "B: REL="), "%x", &relFlags)
		}
		if strings.HasPrefix(line, "H: Handlers=") {
			handlers = strings.Fields(strings.TrimPrefix(line, "H: Handlers="))
		}
	}
	flush()

	if best == nil {
		return "", fmt.Errorf("no mouse device found in /proc/bus/input/devices")
	}
	return best.event, nil
}
