package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-vgo/robotgo"

	"kvm-bodge/internal/proto"
)

func main() {
	server := flag.String("server", "", "server IP or host (required)")
	port := flag.Int("port", 7777, "server port")
	sideStr := flag.String("side", "", "which side of the server this monitor is on: left|right|top|bottom (required)")
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
	var remoteMode atomic.Bool

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
				remoteMode.Store(true)
				ex, ey := entryPos(side, screenW, screenH)
				robotgo.Move(ex, ey)
				log.Println("mouse entered from server")

			case proto.MsgMouseDelta:
				if !remoteMode.Load() || len(m.Payload) < 4 {
					continue
				}
				dx, dy := proto.DecodeMouseDelta(m.Payload)
				cx, cy := robotgo.GetMousePos()
				nx := clamp(cx+dx, 0, screenW-1)
				ny := clamp(cy+dy, 0, screenH-1)
				robotgo.Move(nx, ny)

				// Check if we've hit the return edge.
				if atReturnEdge(nx, ny, side, screenW, screenH) {
					remoteMode.Store(false)
					writeCh <- proto.Message{Type: proto.MsgMouseLeave}
					log.Println("mouse returned to server")
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

// atReturnEdge checks if the cursor has hit the edge that leads back to the server.
func atReturnEdge(x, y int, side byte, w, h int) bool {
	switch side {
	case proto.SideRight:
		return x <= 1
	case proto.SideLeft:
		return x >= w-2
	case proto.SideTop:
		return y >= h-2
	case proto.SideBottom:
		return y <= 1
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
