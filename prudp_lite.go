package nex

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
)

// PRUDP packet types.
const (
	PacketSYN        uint8 = 0
	PacketCONNECT    uint8 = 1
	PacketDATA       uint8 = 2
	PacketDISCONNECT uint8 = 3
	PacketPING       uint8 = 4
)

// PRUDP packet flags.
const (
	FlagACK      uint16 = 0x001
	FlagReliable uint16 = 0x002
	FlagNeedACK  uint16 = 0x004
	FlagHasSize  uint16 = 0x008
	FlagMultiACK uint16 = 0x200
)

// PRUDP-Lite option ids.
const (
	optSupport           uint8 = 0
	optConnectionSig     uint8 = 1
	optConnectionSigLite uint8 = 128
)

// liteMagic is the first byte of every PRUDP-Lite packet.
const liteMagic byte = 0x80

// liteHeaderSize is the fixed PRUDP-Lite header length.
const liteHeaderSize = 12

// liteConnSigKey keys the connection-signature HMAC (a fixed 15-byte constant
// used by the Lite profile). Telle que documentee par NintendoClients (MIT) — voir Credits dans le README.
var liteConnSigKey = mustHex("26c31f381e46d6eb38e1af6ab70d11")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Packet is a decoded PRUDP-Lite packet. Lite carries no session id on the wire
// (it is implicitly 0), and only the CONNECT packet carries a signature.
type Packet struct {
	Type          uint8
	Flags         uint16
	SourceType    uint8
	SourcePort    uint8
	DestType      uint8
	DestPort      uint8
	FragmentID    uint8
	PacketID      uint16
	Signature     []byte // CONNECT (client proof)
	ConnectionSig []byte // SYN+ACK (server signature)
	MinorVersion  uint8
	SupportedFunc uint32
	Payload       []byte
}

// HasFlag reports whether a flag is set.
func (p *Packet) HasFlag(f uint16) bool { return p.Flags&f != 0 }

// EncodePacket serializes a PRUDP-Lite packet.
func EncodePacket(p *Packet) []byte {
	options := encodeLiteOptions(p)

	out := make([]byte, 0, liteHeaderSize+len(options)+len(p.Payload))
	out = append(out, liteMagic, byte(len(options)))
	out = binary.LittleEndian.AppendUint16(out, uint16(len(p.Payload)))
	out = append(out, (p.SourceType<<4)|(p.DestType&0xF))
	out = append(out, p.SourcePort, p.DestPort, p.FragmentID)
	out = binary.LittleEndian.AppendUint16(out, uint16(p.Type&0xF)|(p.Flags<<4))
	out = binary.LittleEndian.AppendUint16(out, p.PacketID)
	out = append(out, options...)
	out = append(out, p.Payload...)
	return out
}

func encodeLiteOptions(p *Packet) []byte {
	var out []byte
	put := func(id byte, val []byte) {
		out = append(out, id, byte(len(val)))
		out = append(out, val...)
	}
	if p.Type == PacketSYN || p.Type == PacketCONNECT {
		support := make([]byte, 4)
		binary.LittleEndian.PutUint32(support, uint32(p.MinorVersion)|(p.SupportedFunc<<8))
		put(optSupport, support)
	}
	if p.Type == PacketSYN && p.HasFlag(FlagACK) {
		put(optConnectionSig, p.ConnectionSig)
	}
	if p.Type == PacketCONNECT && !p.HasFlag(FlagACK) {
		put(optConnectionSigLite, p.Signature)
	}
	return out
}

// DecodePackets parses as many complete PRUDP-Lite packets as buf contains,
// returning the packets and any trailing incomplete bytes (to be prepended to
// the next read). A bad magic byte is a hard error.
func DecodePackets(buf []byte) ([]*Packet, []byte, error) {
	var packets []*Packet
	for {
		if len(buf) < liteHeaderSize {
			break
		}
		if buf[0] != liteMagic {
			return packets, buf, fmt.Errorf("prudp-lite: invalid magic 0x%02x", buf[0])
		}
		optSize := int(buf[1])
		payloadSize := int(binary.LittleEndian.Uint16(buf[2:4]))
		total := liteHeaderSize + optSize + payloadSize
		if len(buf) < total {
			break // wait for the rest of this packet
		}

		p := &Packet{}
		st := buf[4]
		p.SourceType = st >> 4
		p.DestType = st & 0xF
		p.SourcePort = buf[5]
		p.DestPort = buf[6]
		p.FragmentID = buf[7]
		typeFlags := binary.LittleEndian.Uint16(buf[8:10])
		p.Flags = typeFlags >> 4
		p.Type = uint8(typeFlags & 0xF)
		p.PacketID = binary.LittleEndian.Uint16(buf[10:12])

		if err := decodeLiteOptions(p, buf[liteHeaderSize:liteHeaderSize+optSize]); err != nil {
			return packets, buf, err
		}
		p.Payload = append([]byte(nil), buf[liteHeaderSize+optSize:total]...)

		packets = append(packets, p)
		buf = buf[total:]
	}
	return packets, buf, nil
}

func decodeLiteOptions(p *Packet, data []byte) error {
	for i := 0; i < len(data); {
		if i+2 > len(data) {
			return fmt.Errorf("prudp-lite: truncated option header")
		}
		id := data[i]
		size := int(data[i+1])
		i += 2
		if i+size > len(data) {
			return fmt.Errorf("prudp-lite: truncated option value")
		}
		val := data[i : i+size]
		i += size

		switch id {
		case optSupport:
			if size == 4 {
				v := binary.LittleEndian.Uint32(val)
				p.MinorVersion = uint8(v & 0xFF)
				p.SupportedFunc = v >> 8
			}
		case optConnectionSig:
			p.ConnectionSig = append([]byte(nil), val...)
		case optConnectionSigLite:
			p.Signature = append([]byte(nil), val...)
		}
	}
	return nil
}

// ConnectionSignature returns the server's connection signature for a peer
// address ("ip:port"). It is deterministic for an IPv4 peer; for a peer whose
// address cannot be parsed as IPv4 (proxied/IPv6) a random 16-byte value is
// returned — the client only echoes it back, so any stable value works as long
// as the endpoint reuses the one it generated on SYN.
func ConnectionSignature(addr string) []byte {
	if host, portStr, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(host).To4(); ip != nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				data := make([]byte, 6)
				copy(data, ip)
				binary.BigEndian.PutUint16(data[4:], uint16(port))
				return hmacMD5(liteConnSigKey, data)
			}
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

// LitePacketSignature returns the packet signature for a Lite packet. Only a
// CONNECT carrying NEED_ACK is signed — it proves the sender knows the access
// key — computed as HMAC-MD5(md5(accessKey), md5(accessKey) || connectionSig).
// Every other Lite packet is unsigned (nil).
func LitePacketSignature(accessKey string, p *Packet, connectionSig []byte) []byte {
	if p.Type == PacketCONNECT && p.HasFlag(FlagNeedACK) {
		key := md5Sum([]byte(accessKey))
		return hmacMD5(key, append(append([]byte(nil), key...), connectionSig...))
	}
	return nil
}
