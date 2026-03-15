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
	port := flag.Int("port", 7777, "TCP port to listen on")
	flag.Parse()

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	log.Printf("KVM server listening on %s", addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

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
			go handleClient(c)
		}
	}
}

func handleClient(c net.Conn) {
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

	// Receive client info (side).
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

	// --- Set up goroutines ---
	writeCh := make(chan proto.Message, 128)
	errCh := make(chan error, 4)

	// Writer goroutine — serialises all writes to the connection.
	go func() {
		for msg := range writeCh {
			if err := proto.Write(c, msg); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Reader goroutine.
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

	// Mouse poller goroutine.
	var remoteMode atomic.Bool
	screenW, screenH := robotgo.GetScreenSize()
	centerX, centerY := screenW/2, screenH/2

	go func() {
		ticker := time.NewTicker(8 * time.Millisecond) // ~120 Hz
		defer ticker.Stop()
		for range ticker.C {
			x, y := robotgo.GetMousePos()
			if !remoteMode.Load() {
				if atEdge(x, y, side, screenW, screenH) {
					remoteMode.Store(true)
					log.Printf("[%s] mouse leaving to client", remote)
					writeCh <- proto.Message{Type: proto.MsgMouseEnter}
					robotgo.Move(centerX, centerY)
				}
			} else {
				dx := x - centerX
				dy := y - centerY
				if dx != 0 || dy != 0 {
					writeCh <- proto.Message{
						Type:    proto.MsgMouseDelta,
						Payload: proto.EncodeMouseDelta(dx, dy),
					}
					robotgo.Move(centerX, centerY)
				}
			}
		}
	}()

	// Heartbeat ticker.
	heartbeat := time.NewTicker(3 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case err := <-errCh:
			log.Printf("[%s] error: %v", remote, err)
			return

		case <-heartbeat.C:
			writeCh <- proto.Message{Type: proto.MsgHeartbeatPing}

		case m := <-inCh:
			switch m.Type {
			case proto.MsgHeartbeatPong:
				// OK

			case proto.MsgMouseLeave:
				remoteMode.Store(false)
				// Return mouse to the edge we were watching.
				rx, ry := returnPos(side, screenW, screenH)
				robotgo.Move(rx, ry)
				log.Printf("[%s] mouse returned to server", remote)

			case proto.MsgBye:
				log.Printf("[%s] client said bye", remote)
				return
			}
		}
	}
}

// atEdge returns true when (x,y) is at the screen edge corresponding to the client's side.
func atEdge(x, y int, side byte, w, h int) bool {
	switch side {
	case proto.SideRight:
		return x >= w-2
	case proto.SideLeft:
		return x <= 1
	case proto.SideTop:
		return y <= 1
	case proto.SideBottom:
		return y >= h-2
	}
	return false
}

// returnPos gives a sensible mouse position on the server when control returns.
func returnPos(side byte, w, h int) (x, y int) {
	switch side {
	case proto.SideRight:
		return w - 10, h / 2
	case proto.SideLeft:
		return 10, h / 2
	case proto.SideTop:
		return w / 2, 10
	case proto.SideBottom:
		return w / 2, h - 10
	}
	return w / 2, h / 2
}
