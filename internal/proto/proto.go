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
	MsgMouseDelta    MsgType = 0x06 // server→client: int16 dx, int16 dy
	MsgMouseEnter    MsgType = 0x07 // server→client: mouse control transferred
	MsgMouseLeave    MsgType = 0x08 // client→server: return control to server
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

// EncodeMouseDelta packs dx, dy into a 4-byte payload (int16 each, big-endian).
func EncodeMouseDelta(dx, dy int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], uint16(int16(dx)))
	binary.BigEndian.PutUint16(b[2:4], uint16(int16(dy)))
	return b
}

// DecodeMouseDelta unpacks a 4-byte payload into dx, dy.
func DecodeMouseDelta(b []byte) (dx, dy int) {
	dx = int(int16(binary.BigEndian.Uint16(b[0:2])))
	dy = int(int16(binary.BigEndian.Uint16(b[2:4])))
	return
}
