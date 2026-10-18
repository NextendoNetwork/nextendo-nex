package nex

import (
	"bytes"
	"testing"
)

// clientPorts are the virtual source ports the simulated console uses; the
// server echoes its own (dest) ports back as source.
const (
	clientVType = 10
	clientVPort = 15
	serverVType = 10
	serverVPort = 1
)

func synPacket() *Packet {
	return &Packet{
		Type: PacketSYN, Flags: FlagNeedACK,
		SourceType: clientVType, SourcePort: clientVPort,
		DestType: serverVType, DestPort: serverVPort,
		MinorVersion: 5,
	}
}

func connectPacket(accessKey string, connSig, payload []byte) *Packet {
	p := &Packet{
		Type: PacketCONNECT, Flags: FlagNeedACK,
		SourceType: clientVType, SourcePort: clientVPort,
		DestType: serverVType, DestPort: serverVPort,
		MinorVersion: 5, PacketID: 1, Payload: payload,
	}
	p.Signature = LitePacketSignature(accessKey, p, connSig)
	return p
}

func dataPacket(payload []byte, packetID uint16) *Packet {
	return &Packet{
		Type: PacketDATA, Flags: FlagReliable | FlagNeedACK | FlagHasSize,
		SourceType: clientVType, SourcePort: clientVPort,
		DestType: serverVType, DestPort: serverVPort,
		FragmentID: 0, PacketID: packetID, Payload: payload,
	}
}

// decodeOne decodes exactly one packet from a measured frame.
func decodeOne(t *testing.T, frame []byte) *Packet {
	t.Helper()
	packets, _, err := DecodePackets(frame)
	if err != nil || len(packets) != 1 {
		t.Fatalf("decode frame: err=%v n=%d", err, len(packets))
	}
	return packets[0]
}

// TestInsecureHandshakeAndRMC drives an insecure (auth-style) endpoint through
// SYN → CONNECT → an RMC request, checking every response packet.
func TestInsecureHandshakeAndRMC(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	ep.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
		return NewRMCSuccess(c.Settings, req.Protocol, req.Method, req.CallID, []byte{0xAA, 0xBB})
	})

	var sent [][]byte
	conn := NewConnection(ep, "1.2.3.4:5678", func(b []byte) {
		sent = append(sent, append([]byte(nil), b...))
	})

	// SYN → SYN+ACK
	conn.Feed(EncodePacket(synPacket()))
	if len(sent) != 1 {
		t.Fatalf("after SYN: %d packets", len(sent))
	}
	synAck := decodeOne(t, sent[0])
	if synAck.Type != PacketSYN || !synAck.HasFlag(FlagACK) || len(synAck.ConnectionSig) != 16 {
		t.Fatalf("bad SYN+ACK: %+v", synAck)
	}
	// The server must answer as the port the client targeted.
	if synAck.SourcePort != serverVPort || synAck.DestPort != clientVPort {
		t.Fatalf("SYN+ACK ports: src=%d dst=%d", synAck.SourcePort, synAck.DestPort)
	}

	// CONNECT → CONNECT+ACK (insecure: empty payload)
	conn.Feed(EncodePacket(connectPacket(s.AccessKey, synAck.ConnectionSig, nil)))
	if len(sent) != 2 {
		t.Fatalf("after CONNECT: %d packets", len(sent))
	}
	connAck := decodeOne(t, sent[1])
	if connAck.Type != PacketCONNECT || !connAck.HasFlag(FlagACK) || len(connAck.Payload) != 0 {
		t.Fatalf("bad CONNECT+ACK: %+v", connAck)
	}
	if conn.state.Load() != stateConnected {
		t.Fatal("connection not marked connected")
	}

	// DATA(RMC request) → ACK + DATA(RMC response)
	req := NewRMCRequest(s, 0x6D, 1, 42, []byte{0x01, 0x02})
	conn.Feed(EncodePacket(dataPacket(req.Encode(), 1)))
	if len(sent) != 4 {
		t.Fatalf("after DATA: %d packets (want 4: ack + response)", len(sent))
	}
	ack := decodeOne(t, sent[2])
	if ack.Type != PacketDATA || !ack.HasFlag(FlagACK) || ack.PacketID != 1 {
		t.Fatalf("bad DATA ack: %+v", ack)
	}
	respPkt := decodeOne(t, sent[3])
	resp, err := ParseRMC(s, respPkt.Payload)
	if err != nil {
		t.Fatalf("parse response RMC: %v", err)
	}
	if resp.Mode != RMCResponse || resp.IsError || resp.CallID != 42 || resp.Protocol != 0x6D ||
		!bytes.Equal(resp.Body, []byte{0xAA, 0xBB}) {
		t.Fatalf("bad RMC response: %+v", resp)
	}
}
