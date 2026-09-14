package nex

import (
	"fmt"
	"net"
	"sync"
)

// UDPServer is a minimal raw-PRUDP-V1-over-UDP secure server. It exists
// specifically for titles whose client never speaks PRUDP-Lite/WebSocket for
// the secure connection (see prudp_udp.go's package comment) -- MHGU is the
// first and so far only one. It handles the SYN/CONNECT handshake for real;
// DATA payloads are logged, not yet dispatched into the RMC/Endpoint stack
// that the WSS transport uses, so we can first confirm a real client
// completes the handshake at all before building out full RMC routing.
type UDPServer struct {
	Settings *Settings
	// SecureKey is the secure server account's derived key -- same value as
	// Endpoint.SecureKey (Settings.DeriveKey([]byte(password), pid)) -- used
	// to decrypt the Kerberos ticket a client's CONNECT packet carries.
	SecureKey []byte
	// OnData, if set, is called for each decrypted DATA payload once a
	// session's key is known (after a successful CONNECT).
	OnData func(remote *net.UDPAddr, payload []byte)

	conn     *net.UDPConn
	sessions map[string]*udpSession
	mu       sync.Mutex
}

type udpSession struct {
	connSig     []byte
	sessionKey  []byte
	callerPID   uint64
	nextPacket  uint16
}

// ListenUDP opens the raw UDP secure server on port.
func (s *UDPServer) ListenUDP(port int) error {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}
	s.conn = conn
	s.sessions = make(map[string]*udpSession)

	fmt.Printf("[PRUDP-UDP] listening on :%d\n", port)

	buf := make([]byte, 65535)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Printf("[PRUDP-UDP] read error: %v\n", err)
			continue
		}
		data := append([]byte(nil), buf[:n]...)
		go s.handlePacket(remote, data)
	}
}

func (s *UDPServer) handlePacket(remote *net.UDPAddr, data []byte) {
	packets, err := DecodeUDPPackets(data)
	if err != nil {
		fmt.Printf("[PRUDP-UDP] decode error from %s: %v (raw=%x)\n", remote, err, data)
		return
	}
	for _, p := range packets {
		s.handleOne(remote, p)
	}
}

func (s *UDPServer) handleOne(remote *net.UDPAddr, p *UDPPacket) {
	key := remote.String()
	flagNames := ""
	for _, f := range []struct {
		bit  uint16
		name string
	}{{FlagACK, "ACK"}, {FlagReliable, "RELIABLE"}, {FlagNeedACK, "NEED_ACK"}, {FlagHasSize, "HAS_SIZE"}, {FlagMultiACK, "MULTI_ACK"}} {
		if p.HasFlag(f.bit) {
			flagNames += f.name + "|"
		}
	}
	fmt.Printf("[PRUDP-UDP] <- %s type=%d flags=%s session=%d packetID=%d payloadLen=%d\n",
		remote, p.Type, flagNames, p.SessionID, p.PacketID, len(p.Payload))

	switch p.Type {
	case PacketSYN:
		s.handleSYN(remote, p)
	case PacketCONNECT:
		s.handleCONNECT(remote, p)
	case PacketDATA:
		s.handleDATA(remote, p, key)
	case PacketPING:
		s.reply(remote, &UDPPacket{
			Type: PacketPING, Flags: FlagACK,
			SourceType: p.DestType, SourcePort: p.DestPort,
			DestType: p.SourceType, DestPort: p.SourcePort,
			SessionID: p.SessionID, PacketID: p.PacketID,
		}, nil)
	case PacketDISCONNECT:
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
	}
}

func (s *UDPServer) handleSYN(remote *net.UDPAddr, p *UDPPacket) {
	sig := UDPConnectionSignature(remote)
	s.mu.Lock()
	s.sessions[remote.String()] = &udpSession{connSig: sig}
	s.mu.Unlock()

	resp := &UDPPacket{
		Type: PacketSYN, Flags: FlagACK,
		SourceType: p.DestType, SourcePort: p.DestPort,
		DestType: p.SourceType, DestPort: p.SourcePort,
		SessionID:      p.SessionID,
		PacketID:       p.PacketID,
		ConnectionSig:  sig,
		MinorVersion:   0,
		SupportedFunc:  4, // matches PRUDP-Lite's SupportedFunc default (0x4)
		MaxSubstreamID: 0,
	}
	s.reply(remote, resp, nil)
}

func (s *UDPServer) handleCONNECT(remote *net.UDPAddr, p *UDPPacket) {
	s.mu.Lock()
	sess, ok := s.sessions[remote.String()]
	s.mu.Unlock()
	if !ok {
		fmt.Printf("[PRUDP-UDP] CONNECT from %s with no prior SYN, ignoring\n", remote)
		return
	}

	// The client's CONNECT payload carries the Kerberos ticket issued by the
	// auth server -- decrypt it with the secure server's own key to recover
	// the session key and confirm the caller's PID, exactly like the WSS
	// SecureConnection handler does for the ticket-granting round trip.
	if len(p.Payload) > 0 {
		ticket, err := DecryptServerTicket(p.Payload, s.SecureKey, s.Settings)
		if err != nil {
			fmt.Printf("[PRUDP-UDP] CONNECT ticket decrypt failed from %s: %v (payload=%x)\n", remote, err, p.Payload)
		} else {
			sess.sessionKey = ticket.SessionKey
			sess.callerPID = ticket.Source
			fmt.Printf("[PRUDP-UDP] CONNECT ok from %s pid=%d sessionKeyLen=%d\n", remote, ticket.Source, len(ticket.SessionKey))
		}
	} else {
		fmt.Printf("[PRUDP-UDP] CONNECT from %s has EMPTY payload -- no ticket attached\n", remote)
	}

	resp := &UDPPacket{
		Type: PacketCONNECT, Flags: FlagACK,
		SourceType: p.DestType, SourcePort: p.DestPort,
		DestType: p.SourceType, DestPort: p.SourcePort,
		SessionID:           p.SessionID,
		PacketID:            p.PacketID,
		ConnectionSig:        sess.connSig,
		MinorVersion:         0,
		SupportedFunc:        4,
		InitialUnreliableID:  0,
		MaxSubstreamID:       0,
	}
	s.reply(remote, resp, sess.sessionKey)
}

func (s *UDPServer) handleDATA(remote *net.UDPAddr, p *UDPPacket, key string) {
	s.mu.Lock()
	sess, ok := s.sessions[key]
	s.mu.Unlock()
	if !ok || sess.sessionKey == nil {
		fmt.Printf("[PRUDP-UDP] DATA from %s before CONNECT completed, ignoring (payload=%x)\n", remote, p.Payload)
		return
	}

	plain := rc4Crypt(sess.sessionKey, p.Payload)
	fmt.Printf("[PRUDP-UDP] DATA from %s pid=%d decrypted=%x\n", remote, sess.callerPID, plain)

	if p.HasFlag(FlagNeedACK) || p.HasFlag(FlagReliable) {
		ack := &UDPPacket{
			Type: PacketDATA, Flags: FlagACK,
			SourceType: p.DestType, SourcePort: p.DestPort,
			DestType: p.SourceType, DestPort: p.SourcePort,
			SessionID: p.SessionID, PacketID: p.PacketID,
		}
		s.reply(remote, ack, nil)
	}

	if s.OnData != nil {
		s.OnData(remote, plain)
	}
}

func (s *UDPServer) reply(remote *net.UDPAddr, p *UDPPacket, sessionKey []byte) {
	options := encodeUDPOptions(p)
	sig := UDPPacketSignature(s.accessKey(), p, options, sessionKey, p.ConnectionSig)
	p.Signature = sig
	out := EncodeUDPPacket(p)
	if _, err := s.conn.WriteToUDP(out, remote); err != nil {
		fmt.Printf("[PRUDP-UDP] write error to %s: %v\n", remote, err)
		return
	}
	fmt.Printf("[PRUDP-UDP] -> %s type=%d flags=ACK bytes=%d\n", remote, p.Type, len(out))
}

func (s *UDPServer) accessKey() string {
	return s.Settings.AccessKey
}
