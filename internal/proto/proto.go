// Package proto defines the KVM wire protocol.
//
// Frame format:
//
//	[1 byte: MsgType] [2 bytes: payload length, big-endian] [N bytes: payload]
package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

type MsgType byte

const (
	MsgHello         MsgType = 0x01
	MsgHeartbeatPing MsgType = 0x02
	MsgHeartbeatPong MsgType = 0x03
	MsgBye           MsgType = 0x04
	MsgClientInfo    MsgType = 0x05 // client→server: side byte
	MsgMouseDelta    MsgType = 0x06 // server→client: int16 dx, dy, wheelV, wheelH
	MsgMouseEnter    MsgType = 0x07 // server→client: mouse control transferred
	MsgMouseLeave    MsgType = 0x08 // client→server: return control to server
	MsgMouseButton   MsgType = 0x09 // server→client: uint16 button, uint8 state
)

const (
	ServerHello = "KVM-SERVER/1.0"
	ClientHello = "KVM-CLIENT/1.0"
)

// Side values for MsgClientInfo payload.
const (
	SideLeft   byte = 0
	SideRight  byte = 1
	SideTop    byte = 2
	SideBottom byte = 3
)

func SideFromString(s string) (byte, error) {
	switch s {
	case "left":
		return SideLeft, nil
	case "right":
		return SideRight, nil
	case "top":
		return SideTop, nil
	case "bottom":
		return SideBottom, nil
	}
	return 0, fmt.Errorf("unknown side %q: want left|right|top|bottom", s)
}

type Message struct {
	Type    MsgType
	Payload []byte
}

func Write(w io.Writer, msg Message) error {
	if len(msg.Payload) > 0xFFFF {
		return fmt.Errorf("payload too large: %d bytes", len(msg.Payload))
	}
	buf := make([]byte, 3+len(msg.Payload))
	buf[0] = byte(msg.Type)
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(msg.Payload)))
	copy(buf[3:], msg.Payload)
	_, err := w.Write(buf)
	return err
}

func Read(r io.Reader) (Message, error) {
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return Message{}, err
	}
	msgType := MsgType(hdr[0])
	payloadLen := binary.BigEndian.Uint16(hdr[1:3])
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Message{}, err
		}
	}
	return Message{Type: msgType, Payload: payload}, nil
}

// EncodeMouseDelta packs dx, dy, wheelV, wheelH into 8 bytes (int16 each, big-endian).
func EncodeMouseDelta(dx, dy, wv, wh int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:2], uint16(int16(dx)))
	binary.BigEndian.PutUint16(b[2:4], uint16(int16(dy)))
	binary.BigEndian.PutUint16(b[4:6], uint16(int16(wv)))
	binary.BigEndian.PutUint16(b[6:8], uint16(int16(wh)))
	return b
}

// DecodeMouseDelta unpacks an 8-byte payload into dx, dy, wheelV, wheelH.
func DecodeMouseDelta(b []byte) (dx, dy, wv, wh int) {
	dx = int(int16(binary.BigEndian.Uint16(b[0:2])))
	dy = int(int16(binary.BigEndian.Uint16(b[2:4])))
	wv = int(int16(binary.BigEndian.Uint16(b[4:6])))
	wh = int(int16(binary.BigEndian.Uint16(b[6:8])))
	return
}

// EncodeMouseButton packs a button code and pressed state into 3 bytes.
func EncodeMouseButton(button uint16, pressed bool) []byte {
	b := make([]byte, 3)
	binary.BigEndian.PutUint16(b[0:2], button)
	if pressed {
		b[2] = 1
	}
	return b
}

// DecodeMouseButton unpacks a 3-byte payload into button code and pressed state.
func DecodeMouseButton(b []byte) (button uint16, pressed bool) {
	button = binary.BigEndian.Uint16(b[0:2])
	pressed = b[2] != 0
	return
}
