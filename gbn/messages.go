package gbn

import (
	"bytes"
	"io"
)

const (
	SYN    = 0x01
	DATA   = 0x02
	ACK    = 0x03
	FIN    = 0x04
	SYNACK = 0x05
)

type Message interface {
	Serialize() ([]byte, error)
}

type PacketData struct {
	Seq     uint8
	Payload []byte
}

var _ Message = (*PacketData)(nil)

func (m *PacketData) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(DATA); err != nil {
		return nil, err
	}

	if err := buf.WriteByte(m.Seq); err != nil {
		return nil, err
	}

	if _, err := buf.Write(m.Payload); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type PacketACK struct {
	Seq uint8
}

var _ Message = (*PacketACK)(nil)

func (m *PacketACK) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(ACK); err != nil {
		return nil, err
	}

	if err := buf.WriteByte(m.Seq); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type PacketSYN struct {
	N uint8
}

var _ Message = (*PacketSYN)(nil)

func (m *PacketSYN) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(SYN); err != nil {
		return nil, err
	}

	if err := buf.WriteByte(m.N); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type PacketFIN struct {
}

var _ Message = (*PacketFIN)(nil)

func (m *PacketFIN) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(FIN); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type PacketSYNACK struct{}

var _ Message = (*PacketSYNACK)(nil)

func (m *PacketSYNACK) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(SYNACK); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func Deserialize(b []byte) (Message, error) {
	const baseLength = 1
	if len(b) < baseLength {
		return nil, io.EOF
	}

	switch b[0] {
	case DATA:
		if len(b) < 2 {
			return nil, io.EOF
		}
		return &PacketData{
			Seq:     b[1],
			Payload: b[2:],
		}, nil
	case ACK:
		if len(b) < 2 {
			return nil, io.EOF
		}
		return &PacketACK{
			Seq: b[1],
		}, nil
	case SYN:
		if len(b) < 2 {
			return nil, io.EOF
		}
		return &PacketSYN{
			N: b[1],
		}, nil
	case FIN:
		if len(b) < 1 {
			return nil, io.EOF
		}
		return &PacketFIN{}, nil
	case SYNACK:
		if len(b) < 1 {
			return nil, io.EOF
		}
		return &PacketSYNACK{}, nil
	default:
		return nil, io.EOF
	}
}