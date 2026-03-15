package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-vgo/robotgo"

	"kvm-bodge/internal/proto"
)

var debug bool

func dbg(format string, args ...any) {
	if debug {
		log.Printf("[debug] "+format, args...)
	}
}

func main() {
	server := flag.String("server", "", "server IP or host (required)")
	port := flag.Int("port", 7777, "server port")
	sideStr := flag.String("side", "", "which side of the server this monitor is on: left|right|top|bottom (required)")
	flag.BoolVar(&debug, "debug", false, "verbose debug output")
	flag.Parse()

	if *server == "" || *sideStr == "" {
		fmt.Fprintln(os.Stderr, "usage: client --server <ip> --side <left|right|top|bottom> [--port <port>]")
		os.Exit(1)
	}

	side, err := proto.SideFromString(*sideStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	addr := fmt.Sprintf("%s:%d", *server, *port)
	log.Printf("connecting to %s (this monitor is to the %s of server)", addr, *sideStr)

	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// --- Handshake ---
	msg, err := proto.Read(c)
	if err != nil || msg.Type != proto.MsgHello || string(msg.Payload) != proto.ServerHello {
		log.Fatalf("bad server hello")
	}
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgHello,
		Payload: []byte(proto.ClientHello),
	}); err != nil {
		log.Fatalf("hello send: %v", err)
	}
	// Send side info.
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgClientInfo,
		Payload: []byte{side},
	}); err != nil {
		log.Fatalf("client info send: %v", err)
	}
	log.Printf("handshake OK — connected to %s", addr)

	// --- Set up goroutines ---
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

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	screenW, screenH := robotgo.GetScreenSize()
	log.Printf("screen size %dx%d", screenW, screenH)

	// vx, vy track our own virtual cursor position — same approach as the server.
	// We don't rely on robotgo.GetMousePos() because it can be unreliable and
	// robotgo.Move() requires Accessibility permission on macOS (grant it in
	// System Settings → Privacy & Security → Accessibility).
	vx, vy := screenW/2, screenH/2
	remoteMode := false

	for {
		select {
		case <-sig:
			writeCh <- proto.Message{Type: proto.MsgBye}
			time.Sleep(100 * time.Millisecond)
			log.Println("bye")
			return

		case err := <-errCh:
			log.Printf("error: %v", err)
			return

		case m := <-inCh:
			switch m.Type {
			case proto.MsgHeartbeatPing:
				writeCh <- proto.Message{Type: proto.MsgHeartbeatPong}

			case proto.MsgMouseEnter:
				remoteMode = true
				vx, vy = entryPos(side, screenW, screenH)
				robotgo.Move(vx, vy)
				log.Printf("mouse entered from server — placed at (%d,%d)", vx, vy)

			case proto.MsgMouseDelta:
				if !remoteMode || len(m.Payload) < 4 {
					continue
				}
				dx, dy := proto.DecodeMouseDelta(m.Payload)
				vx = clamp(vx+dx, 0, screenW-1)
				vy = clamp(vy+dy, 0, screenH-1)
				robotgo.Move(vx, vy)
				dbg("delta (%+d,%+d) → virtual (%d,%d)", dx, dy, vx, vy)

				// Use push-through return: when virtual pos is clamped at the
				// return edge and another delta still pushes that way.
				if atReturnEdge(vx, vy, dx, dy, side, screenW, screenH) {
					remoteMode = false
					writeCh <- proto.Message{Type: proto.MsgMouseLeave}
					log.Printf("return edge — mouse back to server")
				}

			case proto.MsgBye:
				log.Println("server said bye")
				return
			}
		}
	}
}

// entryPos is where the cursor appears on this screen when the mouse enters from the server.
// The entry side is opposite to where this monitor sits relative to the server.
func entryPos(side byte, w, h int) (x, y int) {
	switch side {
	case proto.SideRight: // client is right → mouse enters from left
		return 2, h / 2
	case proto.SideLeft: // client is left → mouse enters from right
		return w - 2, h / 2
	case proto.SideTop: // client is above → mouse enters from bottom
		return w / 2, h - 2
	case proto.SideBottom: // client is below → mouse enters from top
		return w / 2, 2
	}
	return w / 2, h / 2
}

// atReturnEdge returns true when the virtual position is clamped at the return
// edge and the incoming delta is still pushing toward it (push-through).
func atReturnEdge(x, y, dx, dy int, side byte, w, h int) bool {
	switch side {
	case proto.SideRight: // entered from left → return when pushed back left
		return x == 0 && dx < 0
	case proto.SideLeft: // entered from right → return when pushed back right
		return x == w-1 && dx > 0
	case proto.SideTop: // entered from bottom → return when pushed back down
		return y == h-1 && dy > 0
	case proto.SideBottom: // entered from top → return when pushed back up
		return y == 0 && dy < 0
	}
	return false
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
