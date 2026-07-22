package nex

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// placeholderPIDCounter assigns anonymous PIDs to secure connections that arrive
// without a valid Kerberos ticket (private-test leniency).
var placeholderPIDCounter atomic.Uint64

// fragmentPacing is the delay inserted between successive fragments of a large reliable
// response. Blasting every fragment of a big payload (e.g. SSBU's 15.7 KB DataStore init
// = 13 fragments) at once overflows the console's small PRUDP window, the overflow is
// silently dropped and the message never reassembles. Measured from a measurement of the
// proven server: it emits the 13 fragments in strict order over ~200 ms, one every ~16 ms,
// WITHOUT pausing for acks — so a fixed delay reproduces it exactly. (Ack-driven windowing
// was tried here instead and stretched the same payload to ~2.2 s.)
// Env NEXTENDO_FRAG_PACING_MS overrides it (0 disables). Single-fragment responses
// (the common case) never hit it.
func defaultFragmentPacing() time.Duration {
	ms := 15
	if v := os.Getenv("NEXTENDO_FRAG_PACING_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// retransmitInterval is how often unacknowledged reliable packets are re-sent. It is a
// slow last-resort backstop: a fast interval re-sends fragments the console is still
// reassembling, and the duplicates corrupt/reset its reassembly.
var retransmitInterval = 2 * time.Second

// RMCHandler processes a decoded RMC request and returns the response to send
// back (built with NewRMCSuccess / NewRMCError), or nil to send nothing.
type RMCHandler func(conn *Connection, req *RMCMessage) *RMCMessage

// Endpoint is a NEX service endpoint (one virtual server). It holds the shared
// configuration and the per-protocol RMC handlers; a Connection is created per
// transport socket and references its Endpoint.
type Endpoint struct {
	Settings *Settings

	// Secure, when true, requires each CONNECT to present a valid Kerberos
	// ticket (a game/secure server). SecureKey is the secure account's derived
	// key used to decrypt the ticket. An insecure endpoint (the auth server)
	// leaves both zero and accepts an empty CONNECT payload.
	Secure    bool
	SecureKey []byte

	// FragmentPacing is the per-endpoint delay between successive fragments of
	// a large reliable response. Overrides the global default from
	// NEXTENDO_FRAG_PACING_MS. Zero means no pacing.
	FragmentPacing time.Duration

	// OnConnect, if set, is called once a connection completes its handshake.
	OnConnect func(*Connection)
	// OnDisconnect, if set, is called when a connection is torn down.
	OnDisconnect func(*Connection)
	// OnRMC, if set, is called for every inbound RMC request (for logging).
	OnRMC func(*Connection, *RMCMessage)

	handlers map[uint16]RMCHandler
	mu       sync.RWMutex

	// customPacketHandlers handle non-standard PRUDP packet types (e.g. SSBU's Pia
	// 5.19 type-8 keepalive, which the base SYN/CONNECT/DATA/PING/DISCONNECT switch
	// doesn't know). Keyed by packet type.
	customPacketHandlers map[uint8]func(*Connection, *Packet)

	connections map[uint32]*Connection
	connMu      sync.RWMutex
	connCounter uint32
	reaperOnce  sync.Once
}

// NewEndpoint returns an endpoint bound to the given settings.
func NewEndpoint(settings *Settings) *Endpoint {
	return &Endpoint{
		Settings:             settings,
		FragmentPacing:       defaultFragmentPacing(),
		handlers:             map[uint16]RMCHandler{},
		customPacketHandlers: map[uint8]func(*Connection, *Packet){},
		connections:          map[uint32]*Connection{},
	}
}

// RegisterCustomPacketHandler registers a handler for a non-standard PRUDP packet
// type (beyond SYN/CONNECT/DATA/PING/DISCONNECT). SSBU's Pia 5.19 sends a type-8
// keepalive that must be ACKed or the console treats the connection as dead and
// soft-locks entering "En ligne".
func (e *Endpoint) RegisterCustomPacketHandler(packetType uint8, h func(*Connection, *Packet)) {
	e.customPacketHandlers[packetType] = h
}

// registerConnection assigns a fresh connection id and stores the connection so
// matchmaking/NAT handlers can look it up by id.
func (e *Endpoint) registerConnection(c *Connection) {
	e.connMu.Lock()
	e.connCounter++
	c.ID = e.connCounter
	e.connections[c.ID] = c
	e.connMu.Unlock()
}

func (e *Endpoint) unregisterConnection(c *Connection) {
	if c.ID == 0 {
		return
	}
	e.connMu.Lock()
	delete(e.connections, c.ID)
	e.connMu.Unlock()
}

// FindConnectionByID returns the live connection with the given id, or nil.
func (e *Endpoint) FindConnectionByID(id uint32) *Connection {
	e.connMu.RLock()
	c := e.connections[id]
	e.connMu.RUnlock()
	return c
}

// FindConnectionByPID returns a live connection whose PID matches, or nil. Used to
// push a Participate notification to every player in a gathering by their PID.
func (e *Endpoint) FindConnectionByPID(pid uint64) *Connection {
	e.connMu.RLock()
	defer e.connMu.RUnlock()
	for _, c := range e.connections {
		if c.PID == pid {
			return c
		}
	}
	return nil
}

// SetSecureAccount marks the endpoint secure and derives its ticket key from the
// secure server account's password and PID.
func (e *Endpoint) SetSecureAccount(password string, pid uint64) {
	e.Secure = true
	e.SecureKey = e.Settings.DeriveKey([]byte(password), pid)
}

// Register installs the RMC handler for a protocol id.
func (e *Endpoint) Register(protocolID uint16, h RMCHandler) {
	e.mu.Lock()
	e.handlers[protocolID] = h
	e.mu.Unlock()
}

func (e *Endpoint) handler(protocolID uint16) (RMCHandler, bool) {
	e.mu.RLock()
	h, ok := e.handlers[protocolID]
	e.mu.RUnlock()
	return h, ok
}

// connection state.
const (
	stateConnecting = 0
	stateConnected  = 1
	stateClosed     = 2
)

// Connection is one PRUDP-Lite connection (one transport socket). All inbound
// packets for a socket are fed through Feed from a single goroutine; the mutex
// guards the outgoing counters so cross-connection server pushes stay safe.
type Connection struct {
	Endpoint *Endpoint
	Settings *Settings

	// RemoteAddr is the transport peer address ("ip:port").
	RemoteAddr string

	// ID is the server-assigned connection id (the RVCID matchmaking/NAT use).
	ID uint32
	// PID / CID identify the authenticated user after a secure handshake.
	PID uint64
	CID uint32

	// stationMu guards stationURLs. A connection's stations are written by ITS OWN
	// receive goroutine (Register, ReplaceURL) and read by OTHER connections' goroutines
	// — a joiner asking GetSessionURLs reads the host's. Without this the two race, and
	// the natbridge's wait for the host's ReplaceURL would read it in a tight loop.
	stationMu   sync.RWMutex
	stationURLs []*StationURL

	send func([]byte)

	sessionKey    []byte
	connectionSig []byte

	minorVersion  uint8
	supportedFunc uint32

	// server (source) and client (dest) virtual ports, learned from the SYN.
	srcType, srcPort uint8
	dstType, dstPort uint8

	recvBuf []byte
	fragBuf []byte

	outReliable uint16
	outPing     uint16

	// pending holds sent reliable DATA packets (by sequence id) not yet acknowledged
	// by the console, so a retransmit loop can re-send any the console dropped (its
	// PRUDP sliding window can drop fragments of a large payload it can't buffer in
	// one go). Without this, SSBU's 13-fragment DataStore init loses a fragment and
	// stalls. Removed as the console acks them (single ACK or aggregate MULTI-ACK).
	pending   map[uint16][]byte
	pendingMu sync.Mutex

	state int
	mu    sync.Mutex

	// lastSeen : date (UnixNano) du DERNIER paquet reçu de ce client. Sans elle, rien ne
	// permettait de distinguer un joueur présent d'une connexion morte : une session perdue
	// (crash, coupure, émulateur fermé) restait listée indéfiniment. Relevé en prod le
	// 20/07 : des connexions « actives » depuis 24 h, 45 h, 47 h sans le moindre paquet,
	// qui bloquaient leur propriétaire (« ce compte joue déjà ailleurs ») et faussaient les
	// compteurs. Atomique : écrite sur le chemin chaud de réception, lue par le reaper.
	lastSeen atomic.Int64
}

// IdleFor renvoie depuis combien de temps ce client n'a envoyé aucun paquet.
func (c *Connection) IdleFor() time.Duration {
	ns := c.lastSeen.Load()
	if ns == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ns))
}

// NewConnection creates a connection whose outbound packets are written via send
// (one call per PRUDP packet — one WebSocket binary frame).
func NewConnection(e *Endpoint, remoteAddr string, send func([]byte)) *Connection {
	return &Connection{
		Endpoint:    e,
		Settings:    e.Settings,
		RemoteAddr:  remoteAddr,
		send:        send,
		outReliable: 1,
		outPing:     1,
		pending:     map[uint16][]byte{},
		state:       stateConnecting,
	}
}

// Feed processes raw bytes from the transport (0+ complete packets, buffering a
// trailing partial packet until the rest arrives).
func (c *Connection) Feed(data []byte) {
	c.recvBuf = append(c.recvBuf, data...)
	packets, rest, err := DecodePackets(c.recvBuf)
	if err != nil {
		// Framing is corrupt; drop the buffer and keep the connection so a
		// following clean packet can recover.
		c.recvBuf = nil
		return
	}
	c.recvBuf = rest
	for _, p := range packets {
		c.handlePacket(p)
	}
}

func (c *Connection) handlePacket(p *Packet) {
	// Tout paquet reçu prouve que le client est vivant : c'est la seule marque de vie
	// fiable dont on dispose (voir lastSeen). Le reaper s'appuie dessus pour évincer les
	// connexions mortes au lieu de les laisser s'accumuler.
	c.lastSeen.Store(time.Now().UnixNano())
	switch p.Type {
	case PacketSYN:
		if !p.HasFlag(FlagACK) {
			c.processSYN(p)
		}
	case PacketCONNECT:
		if !p.HasFlag(FlagACK) {
			c.processCONNECT(p)
		}
	case PacketDATA:
		c.processData(p)
	case PacketPING:
		c.processPing(p)
	case PacketDISCONNECT:
		// The console sends DISCONNECT with NeedAck and keeps retransmitting until
		// it receives the matching DISCONNECT+ack. Without that ack the connection
		// never tears down cleanly (retransmit storm) and the client stalls on the
		// close before advancing its online state machine.
		if !p.HasFlag(FlagACK) {
			if p.HasFlag(FlagNeedACK) {
				c.sendAck(p)
			}
			c.close()
		}
	default:
		// Non-standard packet type (e.g. SSBU Pia 5.19 type-8 keepalive): dispatch to
		// a registered custom handler if any; otherwise ignore.
		if h := c.Endpoint.customPacketHandlers[p.Type]; h != nil {
			h(c, p)
		}
	}
}

func (c *Connection) processSYN(p *Packet) {
	// Learn the virtual ports: respond AS the port the client targeted.
	c.srcType, c.srcPort = p.DestType, p.DestPort
	c.dstType, c.dstPort = p.SourceType, p.SourcePort

	c.connectionSig = ConnectionSignature(c.RemoteAddr)
	c.minorVersion = min8(c.Settings.PrudpMinorVersion, p.MinorVersion)
	c.supportedFunc = uint32(c.Settings.SupportedFunctions) & p.SupportedFunc

	ack := &Packet{
		Type: PacketSYN,
		// HasSize belongs on the SYN-ACK, exactly like the CONNECT-ACK below. Without it
		// the client completes the handshake but then sends its CONNECT with an EMPTY
		// payload — it never hands over its Kerberos ticket, so the session is never
		// really authenticated (which is what forced the PID-by-IP workaround). Wire
		// measurement of the secure connection with the client held constant: proven server
		// SYN-ACK ACK|HASSIZE -> CONNECT plen=104; ours SYN-ACK ACK -> CONNECT plen=0,
		// the only remaining difference across the whole handshake.
		Flags:      FlagACK | FlagHasSize,
		SourceType: c.srcType, SourcePort: c.srcPort,
		DestType: c.dstType, DestPort: c.dstPort,
		ConnectionSig: c.connectionSig,
		MinorVersion:  c.minorVersion,
		SupportedFunc: c.supportedFunc,
	}
	c.send(EncodePacket(ack))
}

func (c *Connection) processCONNECT(p *Packet) {
	// The client proves it knows the access key by signing our connection sig.
	expected := LitePacketSignature(c.Settings.AccessKey, p, c.connectionSig)
	if !equalBytes(expected, p.Signature) {
		fmt.Printf("[PRUDP] CONNECT bad signature from %s got=%x want=%x\n", c.RemoteAddr, p.Signature, expected)
		return // invalid — ignore
	}

	response, err := c.processLoginRequest(p.Payload)
	if err != nil {
		fmt.Printf("[PRUDP] CONNECT login failed from %s: %v (payload=%d bytes)\n", c.RemoteAddr, err, len(p.Payload))
		return // bad ticket — ignore
	}
	fmt.Printf("[PRUDP] CONNECT ok from %s pid=%d secure=%v\n", c.RemoteAddr, c.PID, c.Endpoint.Secure)

	ack := &Packet{
		Type:       PacketCONNECT,
		Flags:      FlagACK | FlagHasSize,
		SourceType: c.srcType, SourcePort: c.srcPort,
		DestType: c.dstType, DestPort: c.dstPort,
		MinorVersion:  c.minorVersion,
		SupportedFunc: c.supportedFunc,
		PacketID:      1,
		Payload:       response,
	}
	c.send(EncodePacket(ack))

	c.state = stateConnected
	c.Endpoint.registerConnection(c)
	go c.retransmitLoop() // re-send unacked reliable packets (large-payload reliability)
	if c.Endpoint.OnConnect != nil {
		c.Endpoint.OnConnect(c)
	}
}

// processLoginRequest validates the CONNECT payload. For a secure endpoint the
// payload is [buffer ticket][buffer request]; the ticket is decrypted with the
// secure key to recover the session key + source PID, and the request is
// decrypted with the session key to recover (PID, CID, check). The response is
// [u32 4][u32 check+1], proving the server holds the session key.
func (c *Connection) processLoginRequest(payload []byte) ([]byte, error) {
	if !c.Endpoint.Secure {
		return []byte{}, nil
	}

	// Private-test leniency: the console currently connects to the secure server
	// WITHOUT a Kerberos ticket (empty CONNECT) because the source-key exchange
	// isn't matched to its token derivation yet. Accept it anonymously so it can
	// still reach matchmaking. Per PRUDP, an empty CONNECT expects an empty
	// response (the console does not check a value in this case).
	if len(payload) == 0 {
		if pid, ok := RecallAuthPID(c.RemoteAddr); ok {
			// Same client authed a moment ago on the auth server: reuse that PID so
			// the session it hosts is owned by the console's real identity.
			c.PID = pid
			fmt.Printf("[PRUDP] CONNECT anonymous (no ticket) from %s -> inherited auth pid=%d\n", c.RemoteAddr, c.PID)
		} else if pid, ok := RecallRecentAuthPID(); ok {
			// Per-IP recall missed because the auth was fronted by a proxy (Traefik on :443)
			// so it was remembered under the proxy's IP, while this secure CONNECT arrives on
			// the host-published port with the real IP. Fall back to the just-minted login PID
			// so the console still owns its own session (no placeholder -> no SessionKeepFailed).
			c.PID = pid
			fmt.Printf("[PRUDP] CONNECT anonymous (no ticket) from %s -> recent auth pid=%d\n", c.RemoteAddr, c.PID)
		} else {
			c.PID = 1800000000 + placeholderPIDCounter.Add(1)
			fmt.Printf("[PRUDP] CONNECT anonymous (no ticket) from %s -> placeholder pid=%d\n", c.RemoteAddr, c.PID)
		}
		return []byte{}, nil
	}

	in := NewStreamIn(payload, c.Settings)
	ticketData := in.Buffer()
	requestData := in.Buffer()
	if in.Err() != nil {
		return nil, in.Err()
	}

	ticket, err := DecryptServerTicket(ticketData, c.Endpoint.SecureKey, c.Settings)
	if err != nil {
		return nil, err
	}

	decrypted, err := kerberosDecrypt(ticket.SessionKey, requestData)
	if err != nil {
		return nil, err
	}
	if len(decrypted) != c.Settings.PIDSize+8 {
		return nil, fmt.Errorf("connect: bad check-data size %d", len(decrypted))
	}

	rs := NewStreamIn(decrypted, c.Settings)
	pid := rs.PID()
	if pid != ticket.Source {
		return nil, fmt.Errorf("connect: pid %d != ticket source %d", pid, ticket.Source)
	}
	cid := rs.U32()
	check := rs.U32()

	c.PID = pid
	c.CID = cid
	c.sessionKey = ticket.SessionKey

	out := NewStreamOut(c.Settings)
	out.U32(4)
	out.U32(check + 1)
	return out.Bytes(), nil
}

func (c *Connection) processData(p *Packet) {
	// Aggregate ack (MULTI-ACK): payload = [substreamID u8][extraCount u8][baseSeq u16]
	// [extraSeq u16]*extraCount. baseSeq acks everything up to it; extras are individual.
	if p.HasFlag(FlagMultiACK) {
		pl := p.Payload
		if len(pl) >= 4 {
			count := int(pl[1])
			base := binary.LittleEndian.Uint16(pl[2:4])
			var extra []uint16
			for i, off := 0, 4; i < count && off+2 <= len(pl); i, off = i+1, off+2 {
				extra = append(extra, binary.LittleEndian.Uint16(pl[off:off+2]))
			}
			c.ackPackets(base, extra)
		}
		return
	}
	// Single ack: the packet id it carries is the acknowledged sequence id.
	if p.HasFlag(FlagACK) {
		c.ackPackets(p.PacketID, nil)
		return
	}
	if p.HasFlag(FlagNeedACK) {
		c.sendAck(p)
	}
	// Reassemble reliable fragments in arrival order (WebSocket is in-order).
	c.fragBuf = append(c.fragBuf, p.Payload...)
	if p.FragmentID == 0 {
		full := c.fragBuf
		c.fragBuf = nil
		c.dispatchRMC(full)
	}
}

func (c *Connection) processPing(p *Packet) {
	if p.HasFlag(FlagACK) {
		return
	}
	if p.HasFlag(FlagNeedACK) {
		c.sendAck(p)
	}
}

func (c *Connection) dispatchRMC(payload []byte) {
	req, err := ParseRMC(c.Settings, payload)
	if err != nil || req.Mode != RMCRequest {
		return
	}
	if c.Endpoint.OnRMC != nil {
		c.Endpoint.OnRMC(c, req)
	}
	handler, ok := c.Endpoint.handler(req.Protocol)
	if !ok {
		c.SendRMC(NewRMCError(c.Settings, req.Protocol, req.CallID, ResultCoreNotImplemented))
		return
	}
	if resp := handler(c, req); resp != nil {
		c.SendRMC(resp)
	}
}

// SendRMC serializes and sends an RMC message as reliable DATA (fragmenting as
// needed). Safe to call from any goroutine (used for server→client pushes too).
func (c *Connection) SendRMC(msg *RMCMessage) {
	c.sendData(msg.Encode())
}

// Stations returns the connection's reported station URLs.
//
// A copy of the slice, because the caller reads it while the owning connection may be
// replacing it (ReplaceURL) from its own goroutine. The StationURL pointers themselves are
// shared: nothing mutates one in place after it is stored — a ReplaceURL builds a new
// slice of new urls — so this is enough, and callers that need to modify a url copy it.
func (c *Connection) Stations() []*StationURL {
	c.stationMu.RLock()
	defer c.stationMu.RUnlock()

	return append([]*StationURL(nil), c.stationURLs...)
}

// SetStations replaces the connection's station URLs.
func (c *Connection) SetStations(urls []*StationURL) {
	c.stationMu.Lock()
	c.stationURLs = urls
	c.stationMu.Unlock()
}

func (c *Connection) sendData(data []byte) {
	fragSize := c.Settings.FragmentSize
	if fragSize <= 0 {
		fragSize = 1300
	}
	// Single-fragment (the common case, all MK8 responses): send inline.
	if len(data) <= fragSize {
		c.sendOneFragment(data, 0)
		return
	}
	// Multi-fragment (SSBU's 15 KB DataStore init = 13 fragments): send in a SEPARATE
	// goroutine, paced. This is essential: sendData runs on the connection's single
	// receive goroutine, so pacing inline would block that goroutine and stop it from
	// processing the console's acks DURING the send — the console's sliding window then
	// fills and never drains, the payload never fully arrives and SSBU stalls. Sending
	// async keeps the receive goroutine free to ack the console while we pace fragments.
	go func() {
		fragmentID := uint8(1)
		rest := data
		for {
			if c.state == stateClosed {
				return
			}
			chunk := rest
			if len(chunk) > fragSize {
				chunk = rest[:fragSize]
			} else {
				fragmentID = 0 // last fragment
			}
			c.sendOneFragment(chunk, fragmentID)
			if fragmentID == 0 {
				return
			}
			rest = rest[fragSize:]
			fragmentID++
			// FIXED pacing between fragments, matching the proven server EXACTLY: the
			// the previous stack blasts all 13 fragments of SSBU's 15.7 KB DataStore init in
			// ~200 ms at a steady ~16 ms/fragment, in strict order, WITHOUT stopping to
			// wait for acks. Ack-driven windowing here instead took ~2.2 s, which tripped
			// SSBU's native DataStore-fetch watchdog and crashed Ryujinx (the small
			// single-fragment arena responses were unaffected, which is why arenas worked
			// but quick match didn't). retransmitLoop still backstops any dropped fragment.
			if c.Endpoint.FragmentPacing > 0 {
				time.Sleep(c.Endpoint.FragmentPacing)
			}
		}
	}()
}

// sendOneFragment sends a single reliable DATA fragment, allocating the next
// reliable sequence id under the lock (so concurrent/async sends stay ordered).
func (c *Connection) sendOneFragment(chunk []byte, fragmentID uint8) {
	c.mu.Lock()
	pid := c.outReliable
	pkt := &Packet{
		Type: PacketDATA,
		// The proven server sends DATA fragments with flags 0x6 (Reliable|NeedAck),
		// NOT HasSize (0x8). With HasSize the console reads our plen field per fragment
		// instead of the WebSocket frame length, which corrupts reassembly of a large
		// multi-fragment payload (SSBU's 15 KB DataStore init) — it acks every fragment
		// but never accepts the message. Match the reference exactly.
		Flags:      FlagReliable | FlagNeedACK,
		SourceType: c.srcType, SourcePort: c.srcPort,
		DestType: c.dstType, DestPort: c.dstPort,
		FragmentID: fragmentID,
		PacketID:   pid,
		Payload:    chunk,
	}
	c.outReliable++
	encoded := EncodePacket(pkt)
	c.send(encoded)
	c.mu.Unlock()

	// Remember it for retransmission until the console acks it.
	c.pendingMu.Lock()
	c.pending[pid] = encoded
	c.pendingMu.Unlock()
}

// ackPackets removes acknowledged sequence ids from the pending pool. A base id acks
// everything up to and including it; extra ids are acked individually.
func (c *Connection) ackPackets(base uint16, extra []uint16) {
	c.pendingMu.Lock()
	for pid := range c.pending {
		if pid <= base {
			delete(c.pending, pid)
		}
	}
	for _, pid := range extra {
		delete(c.pending, pid)
	}
	c.pendingMu.Unlock()
}

// retransmitLoop re-sends any unacknowledged reliable packet every retransmitInterval
// until the connection closes. On a reliable WebSocket nothing is lost in transit, but
// the console's PRUDP layer can drop fragments of a payload larger than its sliding
// window; re-sending the ones it never acked lets it complete the reassembly.
func (c *Connection) retransmitLoop() {
	ticker := time.NewTicker(retransmitInterval)
	defer ticker.Stop()
	for range ticker.C {
		if c.state == stateClosed {
			return
		}
		c.pendingMu.Lock()
		if len(c.pending) == 0 {
			c.pendingMu.Unlock()
			continue
		}
		batch := make([][]byte, 0, len(c.pending))
		for _, enc := range c.pending {
			batch = append(batch, enc)
		}
		c.pendingMu.Unlock()
		for _, enc := range batch {
			c.mu.Lock()
			c.send(enc)
			c.mu.Unlock()
		}
	}
}

func (c *Connection) sendAck(p *Packet) {
	ack := &Packet{
		Type:       p.Type,
		Flags:      FlagACK,
		SourceType: c.srcType, SourcePort: c.srcPort,
		DestType: c.dstType, DestPort: c.dstPort,
		FragmentID: p.FragmentID,
		PacketID:   p.PacketID,
	}
	c.send(EncodePacket(ack))
}

func (c *Connection) close() {
	if c.state == stateClosed {
		return
	}
	c.state = stateClosed
	c.Endpoint.unregisterConnection(c)
	if c.Endpoint.OnDisconnect != nil {
		c.Endpoint.OnDisconnect(c)
	}
}

// Close tears down the connection (called by the transport on socket close).
func (c *Connection) Close() { c.close() }

// SendAck sends a PRUDP ACK mirroring the given packet. Exported for custom packet
// handlers (e.g. SSBU's Pia type-8 keepalive, which must be acknowledged).
func (c *Connection) SendAck(p *Packet) { c.sendAck(p) }

func min8(a int, b uint8) uint8 {
	if a < int(b) {
		return uint8(a)
	}
	return b
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
