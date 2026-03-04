package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	Magic   uint16 = 0xCAFE
	Version uint8  = 1

	HeaderSize = 14
)

const (
	CmdAuthReq       uint16 = 1001
	CmdAuthResp      uint16 = 1002
	CmdHeartbeatReq  uint16 = 1003
	CmdHeartbeatResp uint16 = 1004
	CmdGameMessage   uint16 = 2001
	CmdServerPush    uint16 = 3001
	CmdError         uint16 = 9001
)

type Packet struct {
	Cmd     uint16
	Seq     uint32
	Payload []byte
}

func ReadPacket(r io.Reader, maxPayload uint32) (Packet, error) {
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Packet{}, err
	}

	magic := binary.BigEndian.Uint16(header[0:2])
	if magic != Magic {
		return Packet{}, errors.New("bad packet magic")
	}
	ver := header[2]
	if ver != Version {
		return Packet{}, errors.New("unsupported protocol version")
	}

	cmd := binary.BigEndian.Uint16(header[4:6])
	seq := binary.BigEndian.Uint32(header[6:10])
	payloadLen := binary.BigEndian.Uint32(header[10:14])
	if payloadLen > maxPayload {
		return Packet{}, errors.New("payload too large")
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Packet{}, err
	}

	return Packet{
		Cmd:     cmd,
		Seq:     seq,
		Payload: payload,
	}, nil
}

func WritePacket(w io.Writer, p Packet) error {
	payloadLen := len(p.Payload)
	if payloadLen > int(^uint32(0)) {
		return errors.New("payload too large")
	}

	var header [HeaderSize]byte
	binary.BigEndian.PutUint16(header[0:2], Magic)
	header[2] = Version
	header[3] = 0 // flags reserved
	binary.BigEndian.PutUint16(header[4:6], p.Cmd)
	binary.BigEndian.PutUint32(header[6:10], p.Seq)
	binary.BigEndian.PutUint32(header[10:14], uint32(payloadLen))

	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if payloadLen == 0 {
		return nil
	}
	_, err := w.Write(p.Payload)
	return err
}
