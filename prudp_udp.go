package nex

// Raw PRUDP V1 over UDP -- the transport MHGU's legacy 3DS-derived netcode
// actually speaks for its secure connection (confirmed via ARM32 decompile:
// it opens a real nn::socket::Socket(AF_INET, SOCK_DGRAM, 0), never a
// WebSocket, for connectionType 9), and confirmed via Pretendo's proven,
// production MH4U server (github.com/PretendoNetwork/monster-hunter-4-ultimate)
// which listens with a plain PRUDPServer.Listen(port) -- no WSS -- for the
// same game engine lineage. This is a from-scratch port of the V1 packet
// format, ported field-for-field against kinnay/NintendoClients' prudp.py
// (the community reference implementation) rather than guessed.

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"net"
)

// udpLiteConnSigKey is the same fixed 15-byte HMAC key PRUDP-Lite uses for
// connection signatures -- V1 uses the identical constant.
var udpConnSigKey = liteConnSigKey

const udpMagic0 = 0xEA
const udpMagic1 = 0xD0
const udpHeaderSize = 12 // magic(2) is separate; this is version..packet_id
const udpSignatureSize = 16

// UDPPacket is a decoded raw PRUDP V1 packet.
type UDPPacket struct {
	Type                uint8
	Flags               uint16
	SourceType          uint8
	SourcePort          uint8
	DestType            uint8
	DestPort            uint8
	SessionID           uint8
	SubstreamID         uint8
	PacketID            uint16
	Signature           []byte // 16 bytes, HMAC-MD5 packet signature
	ConnectionSig       []byte // CONNECT/SYN option
	MinorVersion        uint8
	SupportedFunc       uint32
	InitialUnreliableID uint16
	MaxSubstreamID      uint8
	FragmentID          uint8
	Payload             []byte
}

func (p *UDPPacket) HasFlag(f uint16) bool { return p.Flags&f != 0 }

// EncodeUDPPacket serializes a raw PRUDP V1 packet.
func EncodeUDPPacket(p *UDPPacket) []byte {
	options := encodeUDPOptions(p)
	header := buildUDPHeader12(p, len(options))

	out := make([]byte, 0, 2+len(header)+udpSignatureSize+len(options)+len(p.Payload))
	out = append(out, udpMagic0, udpMagic1)
	out = append(out, header...)
	sig := p.Signature
	if len(sig) != udpSignatureSize {
		sig = make([]byte, udpSignatureSize)
	}
	out = append(out, sig...)
	out = append(out, options...)
	out = append(out, p.Payload...)
	return out
}

func encodeUDPOptions(p *UDPPacket) []byte {
	var out []byte
	put := func(id byte, val []byte) {
		out = append(out, id, byte(len(val)))
		out = append(out, val...)
	}
	putU := func(id byte, val uint32, size int) {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, val)
		put(id, b[:size])
	}
	switch p.Type {
	case PacketSYN, PacketCONNECT:
		putU(optSupport, uint32(p.MinorVersion)|(p.SupportedFunc<<8), 4)
		put(optConnectionSig, p.ConnectionSig)
		if p.Type == PacketCONNECT {
			putU(optUnreliableSeqID, uint32(p.InitialUnreliableID), 2)
		}
		putU(optMaxSubstreamID, uint32(p.MaxSubstreamID), 1)
	case PacketDATA:
		putU(optFragmentID, uint32(p.FragmentID), 1)
	}
	return out
}

const (
	optFragmentID      uint8 = 2
	optUnreliableSeqID uint8 = 3
	optMaxSubstreamID  uint8 = 4
)

// DecodeUDPPackets parses as many complete raw PRUDP V1 packets as buf
// contains (normally exactly one per UDP datagram).
func DecodeUDPPackets(buf []byte) ([]*UDPPacket, error) {
	var packets []*UDPPacket
	for len(buf) > 0 {
		if len(buf) < 2+udpHeaderSize+udpSignatureSize {
			return packets, fmt.Errorf("prudp-v1: packet too short (%d bytes)", len(buf))
		}
		if buf[0] != udpMagic0 || buf[1] != udpMagic1 {
			return packets, fmt.Errorf("prudp-v1: invalid magic 0x%02x%02x", buf[0], buf[1])
		}
		headerFull := buf[:2+udpHeaderSize+udpSignatureSize]
		_ = headerFull

		version := buf[2]
		if version != 1 {
			return packets, fmt.Errorf("prudp-v1: unsupported version %d", version)
		}
		optSize := int(buf[3])
		payloadSize := int(binary.LittleEndian.Uint16(buf[4:6]))
		src := buf[6]
		dst := buf[7]
		typeFlags := binary.LittleEndian.Uint16(buf[8:10])

		total := 2 + udpHeaderSize + udpSignatureSize + optSize + payloadSize
		if len(buf) < total {
			return packets, fmt.Errorf("prudp-v1: truncated packet (need %d, have %d)", total, len(buf))
		}

		p := &UDPPacket{}
		p.SourceType = src >> 4
		p.SourcePort = src & 0xF
		p.DestType = dst >> 4
		p.DestPort = dst & 0xF
		p.Type = uint8(typeFlags & 0xF)
		p.Flags = typeFlags >> 4
		p.SessionID = buf[10]
		p.SubstreamID = buf[11]
		p.PacketID = binary.LittleEndian.Uint16(buf[12:14])
		p.Signature = append([]byte(nil), buf[14:14+udpSignatureSize]...)

		optStart := 14 + udpSignatureSize
		optData := buf[optStart : optStart+optSize]
		if err := decodeUDPOptions(p, optData); err != nil {
			return packets, err
		}

		payloadStart := optStart + optSize
		p.Payload = append([]byte(nil), buf[payloadStart:payloadStart+payloadSize]...)

		packets = append(packets, p)
		buf = buf[total:]
	}
	return packets, nil
}

func decodeUDPOptions(p *UDPPacket, data []byte) error {
	for i := 0; i < len(data); {
		if i+2 > len(data) {
			return fmt.Errorf("prudp-v1: truncated option header")
		}
		id := data[i]
		size := int(data[i+1])
		i += 2
		if i+size > len(data) {
			return fmt.Errorf("prudp-v1: truncated option value")
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
		case optUnreliableSeqID:
			if size == 2 {
				p.InitialUnreliableID = binary.LittleEndian.Uint16(val)
			}
		case optMaxSubstreamID:
			if size >= 1 {
				p.MaxSubstreamID = val[0]
			}
		case optFragmentID:
			if size >= 1 {
				p.FragmentID = val[0]
			}
		}
	}
	return nil
}

// UDPConnectionSignature computes the server's SYN-ACK connection signature
// for a peer address, identical formula to PRUDP-Lite's ConnectionSignature.
func UDPConnectionSignature(addr *net.UDPAddr) []byte {
	ip4 := addr.IP.To4()
	if ip4 == nil {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		return b
	}
	data := make([]byte, 6)
	copy(data, ip4)
	binary.BigEndian.PutUint16(data[4:], uint16(addr.Port))
	return hmacMD5(udpConnSigKey, data)
}

// buildUDPHeader12 builds the 12-byte V1 header (version..packet_id), the
// same bytes EncodeUDPPacket writes, exposed separately so the signature
// function can take header[4:12] per kinnay's `header[4:]` (drops version,
// option_size, and the 2-byte payload_size -- the signature covers only the
// semantically meaningful fields, not framing metadata).
func buildUDPHeader12(p *UDPPacket, optionSize int) []byte {
	header := make([]byte, 0, udpHeaderSize)
	header = append(header, 1) // PRUDP version
	header = append(header, byte(optionSize))
	header = binary.LittleEndian.AppendUint16(header, uint16(len(p.Payload)))
	header = append(header, (p.SourcePort&0xF)|(p.SourceType<<4))
	header = append(header, (p.DestPort&0xF)|(p.DestType<<4))
	header = binary.LittleEndian.AppendUint16(header, uint16(p.Type&0xF)|(p.Flags<<4))
	header = append(header, p.SessionID)
	header = append(header, p.SubstreamID)
	header = binary.LittleEndian.AppendUint16(header, p.PacketID)
	return header
}

// UDPPacketSignature computes the full-packet HMAC-MD5 signature V1 requires
// on every packet: HMAC-MD5(md5(accessKey), header[4:12] || sessionKey ||
// u32LE(sum(accessKey bytes)) || connectionSig || options || payload).
// Ported field-for-field against kinnay/NintendoClients' prudp.py
// PRUDPMessageV1.calc_packet_signature.
func UDPPacketSignature(accessKey string, p *UDPPacket, options []byte, sessionKey, connectionSig []byte) []byte {
	header := buildUDPHeader12(p, len(options))
	key := md5Sum([]byte(accessKey))
	mac := hmac.New(md5.New, key)
	mac.Write(header[4:])
	mac.Write(sessionKey)
	var sum uint32
	for _, c := range []byte(accessKey) {
		sum += uint32(c)
	}
	sumBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(sumBytes, sum)
	mac.Write(sumBytes)
	mac.Write(connectionSig)
	mac.Write(options)
	mac.Write(p.Payload)
	return mac.Sum(nil)
}

func rc4Crypt(key, data []byte) []byte {
	c, _ := rc4.NewCipher(key)
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}
