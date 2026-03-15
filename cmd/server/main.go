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

	"kvm-bodge/internal/evdev"
	"kvm-bodge/internal/proto"
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
	screen := flag.String("screen", "", "logical screen resolution WxH override (default: auto-detect)")
	scaleFlag := flag.Float64("scale", 0, "display scale factor override, e.g. 1.25 (default: auto-detect)")
	flag.BoolVar(&debug, "debug", false, "verbose debug output")
	flag.Parse()

	// Physical screen size.
	var physW, physH int
	if *screen != "" {
		if _, err := fmt.Sscanf(*screen, "%dx%d", &physW, &physH); err != nil || physW <= 0 || physH <= 0 {
			log.Fatalf("invalid --screen %q: want WxH e.g. 1920x1080", *screen)
		}
	} else {
		var err error
		physW, physH, err = detectScreenSize()
		if err != nil {
			log.Fatalf("auto-detect screen size: %v\nHint: pass --screen WxH to set it manually", err)
		}
	}

	// Scale factor → logical resolution.
	scale := *scaleFlag
	if scale <= 0 {
		scale = detectScaleFactor()
	}
	screenW := int(math.Round(float64(physW) / scale))
	screenH := int(math.Round(float64(physH) / scale))
	log.Printf("physical %dx%d  scale %.2f  logical %dx%d", physW, physH, scale, screenW, screenH)

	// Mouse device.
	mouse, err := evdev.Open(*input)
	if err != nil {
		log.Fatalf("open mouse device: %v", err)
	}
	defer mouse.Close()
	log.Printf("reading mouse from %s", mouse.Device())

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	log.Printf("KVM server listening on %s", addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	deltaCh := make(chan evdev.Delta, 256)
	go func() {
		if err := mouse.ReadEvents(deltaCh); err != nil {
			log.Fatalf("evdev read: %v", err)
		}
	}()

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
			handleClient(c, mouse, deltaCh, screenW, screenH)
		}
	}
}

func handleClient(c net.Conn, mouse *evdev.Reader, deltaCh <-chan evdev.Delta, screenW, screenH int) {
	remote := c.RemoteAddr()
	log.Printf("[%s] connected", remote)
	defer func() {
		c.Close()
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
	errCh := make(chan error, 2)

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

	// Virtual cursor — tracks position from raw evdev deltas.
	// Edge is triggered by "push-through": when the virtual position is already
	// clamped at the boundary and the user keeps pushing in that direction.
	// This is robust to any OS pointer speed/acceleration setting.
	vx, vy := screenW/2, screenH/2
	remoteMode := false

	for {
		select {
		case err := <-errCh:
			log.Printf("[%s] error: %v", remote, err)
			return

		case d := <-deltaCh:
			if !remoteMode {
				nx := clamp(vx+d.DX, 0, screenW-1)
				ny := clamp(vy+d.DY, 0, screenH-1)
				dbg("virtual (%d,%d) → (%d,%d) delta (%+d,%+d)", vx, vy, nx, ny, d.DX, d.DY)

				triggered := pushThrough(vx, vy, d, side, screenW, screenH)
				vx, vy = nx, ny

				if triggered {
					remoteMode = true
					if err := mouse.Grab(); err != nil {
						log.Printf("[%s] grab failed: %v", remote, err)
					}
					log.Printf("[%s] push-through at (%d,%d) — sending mouse to client", remote, vx, vy)
					writeCh <- proto.Message{Type: proto.MsgMouseEnter}
				}
			} else {
				dbg("remote delta (%+d,%+d)", d.DX, d.DY)
				writeCh <- proto.Message{
					Type:    proto.MsgMouseDelta,
					Payload: proto.EncodeMouseDelta(d.DX, d.DY),
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
				vx, vy = returnVirtualPos(side, screenW, screenH)
				log.Printf("[%s] mouse returned — virtual pos (%d,%d)", remote, vx, vy)

			case proto.MsgBye:
				log.Printf("[%s] client said bye", remote)
				return
			}
		}
	}
}

// pushThrough returns true when the virtual position was already at the edge
// and the incoming delta is still pushing in that direction.
func pushThrough(oldX, oldY int, d evdev.Delta, side byte, w, h int) bool {
	switch side {
	case proto.SideRight:
		return oldX == w-1 && d.DX > 0
	case proto.SideLeft:
		return oldX == 0 && d.DX < 0
	case proto.SideBottom:
		return oldY == h-1 && d.DY > 0
	case proto.SideTop:
		return oldY == 0 && d.DY < 0
	}
	return false
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
