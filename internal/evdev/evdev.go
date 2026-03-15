//go:build linux

// Package evdev reads raw mouse events from a Linux evdev device.
// Works correctly under both X11 and Wayland.
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
	evSyn  = 0
	evKey  = 1
	evRel  = 2

	relX      = 0
	relY      = 1
	relWheel  = 8
	relHWheel = 11

	synReport = 0

	BtnLeft   = 0x110
	BtnRight  = 0x111
	BtnMiddle = 0x112
	BtnSide   = 0x113
	BtnExtra  = 0x114
)

// inputEvent mirrors struct input_event from linux/input.h (64-bit layout).
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// Kind distinguishes event types emitted by ReadEvents.
type Kind uint8

const (
	KindMove   Kind = 0 // cursor movement or scroll
	KindButton Kind = 1 // button press or release
)

// Event is emitted by ReadEvents once per EV_SYN batch entry.
type Event struct {
	Kind Kind

	// KindMove
	DX, DY         int
	WheelV, WheelH int

	// KindButton
	Button  uint16
	Pressed bool
}

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

// eviocgrab = _IOW('E', 0x90, int)
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

// ReadEvents blocks, reading events and sending them to ch per EV_SYN batch.
// Button events are emitted as individual KindButton events.
// Movement/scroll is emitted as a single KindMove event at sync time.
func (r *Reader) ReadEvents(ch chan<- Event) error {
	log.Printf("evdev: reading from %s", r.device)
	var ev inputEvent
	var dx, dy, wv, wh int
	type btnEvt struct {
		code    uint16
		pressed bool
	}
	var pendingBtns []btnEvt
	first := true

	for {
		if err := binary.Read(r.f, binary.NativeEndian, &ev); err != nil {
			return err
		}
		if first {
			log.Printf("evdev: first raw event type=%d code=%d value=%d", ev.Type, ev.Code, ev.Value)
			first = false
		}

		switch ev.Type {
		case evRel:
			switch ev.Code {
			case relX:
				dx += int(ev.Value)
			case relY:
				dy += int(ev.Value)
			case relWheel:
				wv += int(ev.Value)
			case relHWheel:
				wh += int(ev.Value)
			}

		case evKey:
			switch ev.Code {
			case BtnLeft, BtnRight, BtnMiddle, BtnSide, BtnExtra:
				pendingBtns = append(pendingBtns, btnEvt{ev.Code, ev.Value != 0})
			}

		case evSyn:
			if ev.Code != synReport {
				continue
			}
			// Emit button events first, in order.
			for _, b := range pendingBtns {
				ch <- Event{Kind: KindButton, Button: b.code, Pressed: b.pressed}
			}
			pendingBtns = pendingBtns[:0]
			// Emit movement/scroll if any.
			if dx != 0 || dy != 0 || wv != 0 || wh != 0 {
				ch <- Event{Kind: KindMove, DX: dx, DY: dy, WheelV: wv, WheelH: wh}
			}
			dx, dy, wv, wh = 0, 0, 0, 0
		}
	}
}

// findMouseDevice returns the most appropriate mouse evdev path.
func findMouseDevice() (string, error) {
	if dev, err := globFirst("/dev/input/by-id/*-event-mouse"); err == nil {
		return dev, nil
	}
	if dev, err := globFirst("/dev/input/by-path/*-event-mouse"); err == nil {
		return dev, nil
	}
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
		event       string
		hasMouse    bool
		hasKeyboard bool
	}

	var (
		evFlags  uint64
		relFlags uint64
		handlers []string
		name     string
		best     *candidate
	)

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
		if strings.Contains(strings.ToLower(name), "keyboard") {
			hasKeyboard = true
		}
		if eventNode == "" {
			return
		}
		c := &candidate{event: eventNode, hasMouse: hasMouse, hasKeyboard: hasKeyboard}
		if best == nil || score(c) > score(best) {
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
