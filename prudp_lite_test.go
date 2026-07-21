package nex

import (
	"bytes"
	"testing"
)

// TestPacketExactLayout locks the 12-byte Lite header layout with a PING+ACK
// that carries no options and no payload.
func TestPacketExactLayout(t *testing.T) {
	p := &Packet{
		Type:       PacketPING,
		Flags:      FlagACK,
		SourceType: 1, SourcePort: 2,
		DestType: 3, DestPort: 4,
		FragmentID: 0,
		PacketID:   7,
	}
	want := []byte{
		0x80,       // magic
		0x00,       // option size
		0x00, 0x00, // payload size
		0x13,       // (srcType<<4)|destType
		0x02,       // src port
		0x04,       // dest port
		0x00,       // fragment id
		0x14, 0x00, // type(4) | flags(1)<<4 = 0x14
		0x07, 0x00, // packet id
	}
	if got := EncodePacket(p); !bytes.Equal(got, want) {
		t.Fatalf("PING+ACK layout: got % x, want % x", got, want)
	}
}

func TestPacketDataRoundTrip(t *testing.T) {
	p := &Packet{
		Type:       PacketDATA,
		Flags:      FlagReliable | FlagNeedACK | FlagHasSize,
		SourceType: 10, SourcePort: 1,
		DestType: 10, DestPort: 1,
		FragmentID: 0,
		PacketID:   1,
		Payload:    []byte("hello nextendo"),
	}
	packets, rest, err := DecodePackets(EncodePacket(p))
	if err != nil || len(packets) != 1 || len(rest) != 0 {
		t.Fatalf("decode: err=%v n=%d rest=%d", err, len(packets), len(rest))
	}
	g := packets[0]
	if g.Type != PacketDATA || g.Flags != (FlagReliable|FlagNeedACK|FlagHasSize) ||
		g.SourcePort != 1 || g.PacketID != 1 || !bytes.Equal(g.Payload, p.Payload) {
		t.Fatalf("data round-trip: %+v", g)
	}
}

// TestDecodeMultipleAndPartial checks the stream framing: two packets in one
// buffer decode to two, and a truncated tail is returned as remainder.
func TestDecodeMultipleAndPartial(t *testing.T) {
	a := EncodePacket(&Packet{Type: PacketPING, Flags: FlagNeedACK, PacketID: 1})
	b := EncodePacket(&Packet{Type: PacketDATA, Flags: FlagReliable, PacketID: 2, Payload: []byte("xy")})
	packets, rest, err := DecodePackets(append(append([]byte{}, a...), b...))
	if err != nil || len(packets) != 2 || len(rest) != 0 {
		t.Fatalf("multi: err=%v n=%d rest=%d", err, len(packets), len(rest))
	}

	full := EncodePacket(&Packet{Type: PacketDATA, Flags: FlagReliable, PacketID: 3, Payload: []byte("abcd")})
	packets, rest, err = DecodePackets(full[:len(full)-2])
	if err != nil || len(packets) != 0 || len(rest) != len(full)-2 {
		t.Fatalf("partial: err=%v n=%d rest=%d", err, len(packets), len(rest))
	}
}

// TestSynAckOptions checks that a SYN+ACK carries OPTION_SUPPORT and the
// connection signature, and that they round-trip.
func TestSynAckOptions(t *testing.T) {
	sig := bytes.Repeat([]byte{0xAB}, 16)
	p := &Packet{Type: PacketSYN, Flags: FlagACK, MinorVersion: 5, SupportedFunc: 0, ConnectionSig: sig}
	packets, _, err := DecodePackets(EncodePacket(p))
	if err != nil || len(packets) != 1 {
		t.Fatalf("decode: err=%v n=%d", err, len(packets))
	}
	g := packets[0]
	if g.MinorVersion != 5 || !bytes.Equal(g.ConnectionSig, sig) {
		t.Fatalf("syn+ack options: minor=%d sig=% x", g.MinorVersion, g.ConnectionSig)
	}
}

// TestConnectLiteSignature checks that a client CONNECT (no ACK) carries the
// lite connection signature option and round-trips.
func TestConnectLiteSignature(t *testing.T) {
	sig := bytes.Repeat([]byte{0xCD}, 16)
	p := &Packet{Type: PacketCONNECT, Flags: FlagNeedACK, MinorVersion: 5, Signature: sig}
	packets, _, err := DecodePackets(EncodePacket(p))
	if err != nil || len(packets) != 1 {
		t.Fatalf("decode: err=%v n=%d", err, len(packets))
	}
	if !bytes.Equal(packets[0].Signature, sig) {
		t.Fatalf("connect lite sig: % x", packets[0].Signature)
	}
}

func TestConnectionSignatureDeterministic(t *testing.T) {
	a := ConnectionSignature("1.2.3.4:60003")
	b := ConnectionSignature("1.2.3.4:60003")
	if !bytes.Equal(a, b) || len(a) != 16 {
		t.Fatalf("connection signature not deterministic: %d", len(a))
	}
	if bytes.Equal(a, ConnectionSignature("1.2.3.5:60003")) {
		t.Fatal("connection signature independent of address")
	}
}

func TestLitePacketSignature(t *testing.T) {
	connSig := ConnectionSignature("1.2.3.4:60003")
	connect := &Packet{Type: PacketCONNECT, Flags: FlagNeedACK}
	a := LitePacketSignature("09c1c475", connect, connSig)
	b := LitePacketSignature("09c1c475", connect, connSig)
	if len(a) != 16 || !bytes.Equal(a, b) {
		t.Fatalf("lite packet signature: len=%d deterministic=%v", len(a), bytes.Equal(a, b))
	}
	// A different access key must change the signature.
	if bytes.Equal(a, LitePacketSignature("deadbeef", connect, connSig)) {
		t.Fatal("signature independent of access key")
	}
	// Non-CONNECT packets are unsigned.
	if LitePacketSignature("09c1c475", &Packet{Type: PacketDATA, Flags: FlagReliable}, connSig) != nil {
		t.Fatal("DATA packet should be unsigned")
	}
}
