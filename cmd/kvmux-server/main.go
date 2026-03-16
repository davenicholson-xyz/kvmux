package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/davenicholson-xyz/kvmux/internal/evdev"
	"github.com/davenicholson-xyz/kvmux/internal/proto"
)

var debug bool

func dbg(format string, args ...any) {
	if debug {
		log.Printf("[debug] "+format, args...)
	}
}

func main() {
	port := flag.Int("port", 7777, "TCP port to listen on")
	input := flag.String("input", "", "evdev device path (default: auto-detect)")
	kbInput := flag.String("keyboard", "", "evdev keyboard device path (default: auto-detect)")
	screen := flag.String("screen", "", "logical screen resolution WxH override (default: auto-detect)")
	scaleFlag := flag.Float64("scale", 0, "display scale factor override, e.g. 1.25 (default: auto-detect)")
	flag.BoolVar(&debug, "debug", false, "verbose debug output")
	flag.Parse()

	// Screen size in xdotool coordinate space.
	// We use xdotool getdisplaygeometry by default so that screenW/H are in
	// the same coordinate space as xdotool getmouselocation — this is what
	// makes edge-position percentages correct.  The old sysfs+scale path is
	// kept as a fallback and for when --screen is given explicitly.
	var screenW, screenH int
	var inputScale float64
	if *screen != "" {
		var physW, physH int
		if _, err := fmt.Sscanf(*screen, "%dx%d", &physW, &physH); err != nil || physW <= 0 || physH <= 0 {
			log.Fatalf("invalid --screen %q: want WxH e.g. 1920x1080", *screen)
		}
		inputScale = *scaleFlag
		if inputScale <= 0 {
			inputScale = 1.0
		}
		screenW = int(math.Round(float64(physW) / inputScale))
		screenH = int(math.Round(float64(physH) / inputScale))
		log.Printf("screen %dx%d (from --screen flag, scale %.2f)", screenW, screenH, inputScale)
	} else if w, h, scale, err := detectScreenSizeHyprland(); err == nil {
		screenW, screenH, inputScale = w, h, scale
		log.Printf("screen %dx%d scale %.2f (hyprctl monitors)", screenW, screenH, inputScale)
	} else {
		log.Printf("hyprland detection unavailable (%v) — falling back to sysfs+scale", err)
		physW, physH, err := detectScreenSize()
		if err != nil {
			log.Fatalf("auto-detect screen size: %v\nHint: pass --screen WxH to set it manually", err)
		}
		inputScale = *scaleFlag
		if inputScale <= 0 {
			inputScale = detectScaleFactor()
		}
		screenW = int(math.Round(float64(physW) / inputScale))
		screenH = int(math.Round(float64(physH) / inputScale))
		log.Printf("screen: physical %dx%d scale %.2f → logical %dx%d", physW, physH, inputScale, screenW, screenH)
	}

	// Mouse device.
	mouse, err := evdev.Open(*input)
	if err != nil {
		log.Fatalf("open mouse device: %v", err)
	}
	defer mouse.Close()
	log.Printf("reading mouse from %s", mouse.Device())

	// Keyboard devices (optional, may be multiple).
	var keyboards []*evdev.Reader
	if kbs, err := evdev.OpenKeyboards(*kbInput); err != nil {
		log.Printf("keyboard not found: %v — keyboard forwarding disabled", err)
	} else {
		keyboards = kbs
		for _, kb := range keyboards {
			defer kb.Close()
			log.Printf("reading keyboard from %s", kb.Device())
		}
	}

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	log.Printf("KVM server listening on %s", addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	evCh := make(chan evdev.Event, 256)
	go func() {
		if err := mouse.ReadEvents(evCh); err != nil {
			log.Fatalf("evdev read: %v", err)
		}
	}()

	kbCh := make(chan evdev.Event, 256)
	for _, kb := range keyboards {
		kb := kb
		go func() {
			if err := kb.ReadEvents(kbCh); err != nil {
				log.Printf("evdev keyboard read %s: %v", kb.Device(), err)
			}
		}()
	}

	connCh := make(chan net.Conn)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			connCh <- c
		}
	}()

	for {
		select {
		case <-sig:
			log.Println("shutting down")
			return
		case c := <-connCh:
			handleClient(c, mouse, keyboards, evCh, kbCh, screenW, screenH, inputScale)
		}
	}
}

func handleClient(c net.Conn, mouse *evdev.Reader, keyboards []*evdev.Reader, evCh <-chan evdev.Event, kbCh <-chan evdev.Event, screenW, screenH int, inputScale float64) {
	remote := c.RemoteAddr()
	log.Printf("[%s] connected", remote)
	remoteMode := false
	defer func() {
		c.Close()
		if remoteMode {
			if err := mouse.Ungrab(); err != nil {
				log.Printf("[%s] ungrab on disconnect: %v", remote, err)
			}
			for _, kb := range keyboards { kb.Ungrab() }
			log.Printf("[%s] ungrabbed devices on disconnect", remote)
		}
		log.Printf("[%s] disconnected", remote)
	}()

	// --- Handshake ---
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgHello,
		Payload: []byte(proto.ServerHello),
	}); err != nil {
		log.Printf("[%s] hello send: %v", remote, err)
		return
	}
	msg, err := proto.Read(c)
	if err != nil || msg.Type != proto.MsgHello || string(msg.Payload) != proto.ClientHello {
		log.Printf("[%s] bad hello", remote)
		return
	}
	msg, err = proto.Read(c)
	if err != nil || msg.Type != proto.MsgClientInfo || len(msg.Payload) < 1 {
		log.Printf("[%s] bad client info", remote)
		return
	}
	side := msg.Payload[0]
	sideNames := map[byte]string{
		proto.SideLeft: "left", proto.SideRight: "right",
		proto.SideTop: "top", proto.SideBottom: "bottom",
	}
	log.Printf("[%s] handshake OK — client is to the %s", remote, sideNames[side])

	writeCh := make(chan proto.Message, 128)
	errCh := make(chan error, 4)

	go func() {
		for msg := range writeCh {
			if err := proto.Write(c, msg); err != nil {
				errCh <- err
				return
			}
		}
	}()

	inCh := make(chan proto.Message, 32)
	go func() {
		for {
			m, err := proto.Read(c)
			if err != nil {
				errCh <- err
				return
			}
			inCh <- m
		}
	}()

	// Warp cursor to centre and seed the virtual tracker there.
	// We don't read back from xdotool — its coordinate space differs from our
	// logical screen when XWayland applies its own scaling.
	warpMouseToCenter(screenW, screenH)
	vx, vy := screenW/2, screenH/2
	pressedButtons := map[uint16]bool{}

	for {
		select {
		case err := <-errCh:
			log.Printf("[%s] error: %v", remote, err)
			return

		case ev := <-evCh:
			switch ev.Kind {
			case evdev.KindMove:
				if !remoteMode {
					if ax, ay, ok := readCursorPosHyprland(screenW, screenH); ok {
						vx, vy = ax, ay
					}
					dbg("actual pos (%d,%d) delta (%+d,%+d)", vx, vy, ev.DX, ev.DY)
					triggered := pushThrough(vx, vy, ev, side, screenW, screenH)
					if triggered && len(pressedButtons) == 0 {
						remoteMode = true
						if err := mouse.Grab(); err != nil {
							log.Printf("[%s] grab failed: %v", remote, err)
						}
						for _, kb := range keyboards {
							if err := kb.Grab(); err != nil {
								log.Printf("[%s] keyboard grab %s failed: %v", remote, kb.Device(), err)
							}
						}
						pct := edgePosPct(vx, vy, side, screenW, screenH)
						log.Printf("[%s] \u2192 client (%.1f%%)", remote, pct*100)
						writeCh <- proto.Message{Type: proto.MsgMouseEnter, Payload: proto.EncodeEdgePos(pct)}
					}
				} else {
					dbg("remote move (%+d,%+d) scroll(%+d,%+d)", ev.DX, ev.DY, ev.WheelV, ev.WheelH)
					writeCh <- proto.Message{
						Type:    proto.MsgMouseDelta,
						Payload: proto.EncodeMouseDelta(ev.DX, ev.DY, ev.WheelV, ev.WheelH),
					}
				}

			case evdev.KindButton:
				if ev.Pressed {
					pressedButtons[ev.Button] = true
				} else {
					delete(pressedButtons, ev.Button)
				}
				if remoteMode {
					dbg("remote button %d pressed=%v", ev.Button, ev.Pressed)
					writeCh <- proto.Message{
						Type:    proto.MsgMouseButton,
						Payload: proto.EncodeMouseButton(ev.Button, ev.Pressed),
					}
				}
			}

		case ev := <-kbCh:
			if ev.Kind == evdev.KindKey {
				dbg("key %d pressed=%v", ev.Button, ev.Pressed)
			}
			if remoteMode && ev.Kind == evdev.KindKey {
				writeCh <- proto.Message{
					Type:    proto.MsgKeyEvent,
					Payload: proto.EncodeMouseButton(ev.Button, ev.Pressed),
				}
			}

		case m := <-inCh:
			switch m.Type {
			case proto.MsgHeartbeatPong:
				// OK

			case proto.MsgMouseLeave:
				remoteMode = false
				if err := mouse.Ungrab(); err != nil {
					log.Printf("[%s] ungrab failed: %v", remote, err)
				}
				for _, kb := range keyboards { kb.Ungrab() }
				if len(m.Payload) >= 2 {
					pct := proto.DecodeEdgePos(m.Payload)
					vx, vy = returnVirtualPosFromPct(side, screenW, screenH, pct)
					log.Printf("[%s] ← server (%.1f%%)", remote, pct*100)
				} else {
					vx, vy = returnVirtualPos(side, screenW, screenH)

				}
				// Correct drift with actual Wayland cursor position.
				if ax, ay, ok := readCursorPosHyprland(screenW, screenH); ok {
					vx, vy = ax, ay
				dbg("resynced pos from hyprctl: (%d,%d)", vx, vy)
				}
			case proto.MsgBye:
			dbg("client said bye")
				return
			}
		}
	}
}

// pushThrough returns true when the virtual position was already at the edge
// and the incoming delta is still pushing in that direction.
func pushThrough(oldX, oldY int, ev evdev.Event, side byte, w, h int) bool {
	switch side {
	case proto.SideRight:
		return oldX == w-1 && ev.DX > 0
	case proto.SideLeft:
		return oldX == 0 && ev.DX < 0
	case proto.SideBottom:
		return oldY == h-1 && ev.DY > 0
	case proto.SideTop:
		return oldY == 0 && ev.DY < 0
	}
	return false
}

// edgePosPct returns a 0.0–1.0 fraction representing where along the crossing
// edge the cursor is. For left/right crossings it's the Y fraction; for
// top/bottom crossings it's the X fraction.
func edgePosPct(vx, vy int, side byte, w, h int) float64 {
	switch side {
	case proto.SideRight, proto.SideLeft:
		if h <= 1 {
			return 0.5
		}
		return float64(vy) / float64(h-1)
	case proto.SideTop, proto.SideBottom:
		if w <= 1 {
			return 0.5
		}
		return float64(vx) / float64(w-1)
	}
	return 0.5
}

// returnVirtualPosFromPct places the virtual cursor back at the return edge
// using the percentage received from the client.
func returnVirtualPosFromPct(side byte, w, h int, pct float64) (x, y int) {
	switch side {
	case proto.SideRight:
		return w - 20, int(pct * float64(h-1))
	case proto.SideLeft:
		return 20, int(pct * float64(h-1))
	case proto.SideTop:
		return int(pct * float64(w-1)), 20
	case proto.SideBottom:
		return int(pct * float64(w-1)), h - 20
	}
	return w / 2, h / 2
}


func returnVirtualPos(side byte, w, h int) (x, y int) {
	switch side {
	case proto.SideRight:
		return w - 20, h / 2
	case proto.SideLeft:
		return 20, h / 2
	case proto.SideTop:
		return w / 2, 20
	case proto.SideBottom:
		return w / 2, h - 20
	}
	return w / 2, h / 2
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// detectScaleFactor tries to read the display scale from KDE kwinrc.
// Falls back to 1.0 if nothing is found.
func detectScaleFactor() float64 {
	// When running under sudo, check the real user's config.
	username := os.Getenv("SUDO_USER")
	if username == "" {
		username = os.Getenv("USER")
	}
	var homeDir string
	if u, err := user.Lookup(username); err == nil {
		homeDir = u.HomeDir
	}
	if homeDir == "" {
		return 1.0
	}

	// KDE stores per-output scale in kwinrc under [Outputs][<name>] Scale=X
	kwinrc := filepath.Join(homeDir, ".config", "kwinrc")
	if scale, ok := parseScaleFromKwinrc(kwinrc); ok {
		log.Printf("detected scale %.2f from %s", scale, kwinrc)
		return scale
	}

	return 1.0
}

func parseScaleFromKwinrc(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	// Scan all lines — Scale= can appear in [Xwayland], [Outputs][...], etc.
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Scale=") {
			var scale float64
			if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(line), "Scale="), "%f", &scale); err == nil && scale > 0 {
				return scale, true
			}
		}
	}
	return 0, false
}

// detectScreenSize reads the first connected output's preferred mode from sysfs.
func detectScreenSize() (w, h int, err error) {
	files, _ := filepath.Glob("/sys/class/drm/*/modes")
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) == 0 {
			continue
		}
		line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
		if _, err := fmt.Sscanf(line, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
			log.Printf("detected screen from %s: %dx%d", f, w, h)
			return w, h, nil
		}
	}
	return 0, 0, fmt.Errorf("no resolution found in /sys/class/drm/*/modes")
}
