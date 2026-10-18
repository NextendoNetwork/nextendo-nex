package nex

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// fragmentPacing is the delay inserted between successive fragments of a large reliable
// response. Blasting every fragment of a big payload (e.g. SSBU's 15.7 KB DataStore init
// = 13 fragments) at once overflows the console's small PRUDP window, the overflow is
// silently dropped and the message never reassembles. Measured from a measurement of the
// proven server: it emits the 13 fragments in strict order over ~200 ms, one every ~16 ms,
// WITHOUT pausing for acks — so a fixed delay reproduces it exactly. (Ack-driven windowing
// was tried here instead and stretched the same payload to ~2.2 s.)
// Env NEXTENDO_FRAG_PACING_MS overrides it (0 disables). Single-fragment responses
// (the common case) never hit it.
var fragmentPacing = func() time.Duration {
	ms := 15
	if v := os.Getenv("NEXTENDO_FRAG_PACING_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}()

// retransmitInterval is how often unacknowledged reliable packets are re-sent. It is a
// slow last-resort backstop: a fast interval re-sends fragments the console is still
// reassembling, and the duplicates corrupt/reset its reassembly.
var retransmitInterval = 2 * time.Second

// notifDiagRetransmit active la trace de reemission ci-dessous (NEX_DIAG_RETRANSMIT=1).
var notifDiagRetransmit = os.Getenv("NEX_DIAG_RETRANSMIT") == "1"

// RMCHandler processes a decoded RMC request and returns the response to send
// back (built with NewRMCSuccess / NewRMCError), or nil to send nothing.
type RMCHandler func(conn *Connection, req *RMCMessage) *RMCMessage

// Endpoint is a NEX service endpoint (one virtual server). It holds the shared
// configuration and the per-protocol RMC handlers; a Connection is created per
// transport socket and references its Endpoint.
type Endpoint struct {
	Settings *Settings

	// Secure, when true, marks a game/secure server: each CONNECT goes through the
	// CONNECT verifier (verification_publique.go; none in the public version = refused).
	// SecureKey is the secure account's derived key. An insecure endpoint (the auth server)
	// leaves both zero and accepts an empty CONNECT payload.
	Secure    bool
	SecureKey []byte

	// OnConnect, if set, is called once a connection completes its handshake.
	OnConnect func(*Connection)
	// OnDisconnect, if set, is called when a connection is torn down.
	OnDisconnect func(*Connection)
	// OnRMC, if set, is called for every inbound RMC request (for logging).
	OnRMC func(*Connection, *RMCMessage)
	// OnNATProperties, if set, is called when a client reports its NAT behaviour +
	// ping via NATTraversal ReportNATProperties, so the monitoring dashboard can show
	// each player's NAT type and latency (lost when the games moved off the the previous stack stack).
	OnNATProperties func(pid uint64, natMap, natFilter, rtt uint32)

	handlers map[uint16]RMCHandler
	// fallbackHandler, when set, answers protocols with no registered handler.
	// nil by default: unregistered protocols keep returning NotImplemented.
	fallbackHandler RMCHandler
	mu              sync.RWMutex

	// customPacketHandlers handle non-standard PRUDP packet types (e.g. SSBU's Pia
	// 5.19 type-8 keepalive, which the base SYN/CONNECT/DATA/PING/DISCONNECT switch
	// doesn't know). Keyed by packet type.
	customPacketHandlers map[uint8]func(*Connection, *Packet)

	connections map[uint32]*Connection
	connMu      sync.Mutex
	connCounter uint32

	// MaxRMCSize, si > 0, borne la taille d'une requete RMC reassemblee depuis ses
	// fragments : au-dela, la requete est jetee (une ligne de journal), la connexion reste.
	// Sans borne, une suite de fragments sans fin faisait grossir le tampon de reassemblage
	// sans limite. 0 (defaut) : pas de borne, comportement d'avant. En fin de structure :
	// les champs d'avant gardent leur position.
	MaxRMCSize int

	// partis : trace des connexions fermees depuis moins de relayVuTTL (connpartie.go).
	partisMu     sync.Mutex
	partis       map[uint32]connPartie
	partisMenage time.Time
}

// NewEndpoint returns an endpoint bound to the given settings.
func NewEndpoint(settings *Settings) *Endpoint {
	return &Endpoint{
		Settings:             settings,
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
	e.noterPartie(c)
}

// FindConnectionByID returns the live connection with the given id, or nil.
func (e *Endpoint) FindConnectionByID(id uint32) *Connection {
	e.connMu.Lock()
	c := e.connections[id]
	e.connMu.Unlock()
	return c
}

// FindConnectionByPID returns the MOST RECENT live connection whose PID matches, or nil.
// Used to push a Participate notification to every player in a gathering by their PID.
//
// A player who reconnects (very common: leaving/re-entering online, a dropped WebSocket that
// re-CONNECTs) briefly has TWO connections under the same PID in the map until the old one is
// reaped. Returning "the first match" iterated a Go map — whose order is RANDOMISED — so it
// handed back a STALE connection about half the time. The killer symptom: when a visitor joins,
// the host's Participate notification (which tells its Pia to add the visitor to the mesh
// StationIdStatusTable) went to the dead connection, so the LIVE host never enrolled the visitor
// and silently dropped its P2P game traffic — while still answering the low-level NAT probe
// (that path uses FindConnectionByID on the exact RVCID = the live connection), giving a
// "hole-punch ok" that masks the real failure. Net effect for ACNH: 2618-0502 "other consoles
// not responding". Always hand back the highest-ID (newest) connection for the PID.
func (e *Endpoint) FindConnectionByPID(pid uint64) *Connection {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	var newest *Connection
	matches := 0
	for _, c := range e.connections {
		if c.PID == pid {
			matches++
			if newest == nil || c.ID > newest.ID {
				newest = c
			}
		}
	}
	if matches > 1 {
		fmt.Printf("[endpoint/diag] FindConnectionByPID(%d): %d connexions (DOUBLON perime) -> garde id=%d\n", pid, matches, newest.ID)
	}
	return newest
}

// SetSecureAccount marks the endpoint secure and derives its ticket key from the
// secure server account's password and PID.
func (e *Endpoint) SetSecureAccount(password string, pid uint64) {
	e.Secure = true
	e.SecureKey = e.Settings.DeriveKey([]byte(password), pid)
}

// ConnectionIDs returns the ids of all live connections.
func (e *Endpoint) ConnectionIDs() []uint32 {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	ids := make([]uint32, 0, len(e.connections))
	for id := range e.connections {
		ids = append(ids, id)
	}
	return ids
}

// RegisterFallback sets a handler called when no protocol-specific handler is
// registered. nil (the default) keeps the NotImplemented answer.
func (e *Endpoint) RegisterFallback(h RMCHandler) {
	e.mu.Lock()
	e.fallbackHandler = h
	e.mu.Unlock()
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
	if !ok && e.fallbackHandler != nil {
		h, ok = e.fallbackHandler, true
	}
	e.mu.RUnlock()
	return h, ok
}

// connection state.
const (
	stateConnecting = 0
	stateConnected  = 1
	stateClosed     = 2
)

// Connection is one PRUDP-Lite connection (one transport socket). gws (ParallelEnabled)
// can call Feed for the same socket from up to 8 goroutines at once: recvMu guards the
// receive buffer, fragMu the fragment reassembly, state and the port-secure flags are
// atomic. The mutex guards the outgoing counters so cross-connection server pushes stay
// safe.
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

	// natMapV / natFilterV cache the player's NAT behaviour from ReportNATProperties
	// (protocol 0x03 / method 5). Written by THIS connection's own receive goroutine and
	// read by OTHER connections' goroutines during NAT-aware matchmaking — hence atomic.
	natMapV    atomic.Uint32
	natFilterV atomic.Uint32

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

	state atomic.Int32 // stateConnecting (zero value), stateConnected, stateClosed
	mu    sync.Mutex

	// lastSeen : date (UnixNano) du DERNIER paquet reçu de ce client. Sans elle, rien ne
	// permettait de distinguer un joueur présent d'une connexion morte : une session perdue
	// (crash, coupure, émulateur fermé) restait listée indéfiniment. Relevé en prod le
	// 20/07 : des connexions « actives » depuis 24 h, 45 h, 47 h sans le moindre paquet,
	// qui bloquaient leur propriétaire (« ce compte joue déjà ailleurs ») et faussaient les
	// compteurs. Atomique : écrite sur le chemin chaud de réception, lue par le reaper.
	lastSeen atomic.Int64

	// natMu / nat : etat « pret » (ReplaceURL fait depuis la derniere remise a zero), les
	// InitiateProbe retenus pour cette connexion et ses sondages sans verdict. Voir natpret.go.
	// En fin de structure : les champs d'avant gardent leur position.
	natMu sync.Mutex
	nat   natEtat

	// fragRejet : la requete en cours de reassemblage a depasse Endpoint.MaxRMCSize ; ses
	// fragments suivants sont jetes jusqu'au dernier (FragmentID 0). journalAvantConnect :
	// la DATA recue avant le CONNECT a ete journalisee (une ligne par connexion).
	fragRejet           bool
	journalAvantConnect atomic.Bool

	// connecteUneFois : un CONNECT a ete accepte sur cette connexion (ticket valide sur le
	// port secure). Jamais remis a faux : une connexion connectee puis fermee (reaper, kick,
	// DISCONNECT) reste servie comme avant. La garde du port secure s'appuie sur lui et non
	// sur l'etat : close() fait passer une connexion jamais connectee a « fermee », et
	// l'ancienne garde (etat « en connexion ») laissait alors passer SYN, DISCONNECT puis
	// DATA sous le PID 0. Atomique : ecrit par processCONNECT, lu a chaque DATA, et gws
	// traite jusqu'a 8 messages a la fois par socket.
	connecteUneFois atomic.Bool

	// recvMu : recvBuf (Feed). fragMu : fragBuf et fragRejet (processData). Sans eux, deux
	// messages d'une meme socket traites en meme temps par gws ecrivaient le meme tampon :
	// une requete pouvait etre decodee et servie deux fois (TestFeedEnParalleleSurUneSocket :
	// 870 requetes servies pour 800 envoyees). Les gestionnaires RMC restent appeles hors
	// verrou, en parallele comme avant.
	recvMu sync.Mutex
	fragMu sync.Mutex

	// connectMu serialise les CONNECT d'une connexion (gws en traite plusieurs a la fois).
	// cycleMu ordonne l'entree (enregistrement, OnConnect) et la sortie (close, OnDisconnect)
	// : OnDisconnect ne part jamais avant OnConnect. close() ne prend que cycleMu, jamais
	// connectMu : close() part de la boucle de lecture de gws (OnClose, reader.go) ou du
	// reaper, pendant qu'un CONNECT peut tenir connectMu. Une erreur d'ecriture de gws 1.10.0
	// (WriteMessage, conn.go) marque seulement la socket fermee, sans appeler OnClose.
	connectMu sync.Mutex
	cycleMu   sync.Mutex
	// connectPayload / connectPID : le CONNECT accepte (charge utile et PID prouve par son
	// ticket). journalConnect : un CONNECT refuse apres le premier a ete journalise (une
	// ligne par connexion).
	connectPayload []byte
	connectAck     []byte
	journalConnect atomic.Bool

	// rmcEnVol : gestionnaires RMC en cours pour cette connexion (lancerRMC), pose par le
	// transport WebSocket (Server.OnOpen). nil : appel direct, dans l'appelant de Feed (tests,
	// usages sans transport), comme avant.
	rmcEnVol chan struct{}

	// SansTicket : selon le verificateur de CONNECT (verification_publique.go), l'identite de
	// cette connexion ne vient pas d'un ticket. Pose avant OnConnect, jamais modifie ensuite.
	SansTicket bool

	// socketProlongee : derniere fois (UnixNano) que le transport WebSocket a repousse
	// l'echeance de lecture de la socket (server.go, prolongerSocket).
	socketProlongee atomic.Int64

	// seqRecus : numeros de sequence des DATA fiables deja recues, une case par
	// PacketID%256, qui garde PacketID+1 (0 = case jamais servie). Sous fragMu. Une DATA
	// reemise par la console (ACK perdu ou en retard) porte le meme numero : elle est
	// acquittee de nouveau et jetee (processData). Voir seqDejaRecu.
	seqRecus [256]uint32

	// natmSonde : natm de la derniere station que la console a fait sonder
	// (RequestProbeInitiationExt) ; repli de natmStation. Ecrit par sa goroutine, lu par
	// celles des autres (verdicts, appariement) : atomique.
	natmSonde atomic.Int32
}

// rmcEnVolMax : gestionnaires RMC simultanes par connexion, la borne de gws avant
// (ParallelGolimit 8). Au-dela, la lecture de la socket attend qu'un gestionnaire finisse.
const rmcEnVolMax = 8

// IdleFor renvoie depuis combien de temps ce client n'a envoyé aucun paquet.
func (c *Connection) IdleFor() time.Duration {
	ns := c.lastSeen.Load()
	if ns == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ns))
}

// SetNATProps records the NAT mapping/filtering behaviour the client reported via
// ReportNATProperties, for NAT-aware matchmaking.
func (c *Connection) SetNATProps(natMap, natFilter uint32) {
	c.natMapV.Store(natMap)
	c.natFilterV.Store(natFilter)
}

// NATProps returns the last-reported NAT mapping/filtering behaviour (0,0 if the client
// hasn't reported yet — treated as "unknown" by the matchmaker, which then stays optimistic).
func (c *Connection) NATProps() (natMap, natFilter uint32) {
	return c.natMapV.Load(), c.natFilterV.Load()
}

// NATBehaviour returns the NAT mapping/filtering to classify this client for matchmaking. It
// prefers the ReportNATProperties values (Splatoon 2 sends 0x03/5), and falls back to the
// natm/natf the client registers in its station URLs — Mario Kart never sends ReportNATProperties,
// so without this fallback NAT-aware matchmaking would see every MK8 player as "unknown". Returns
// (0,0) when nothing is known yet (the matchmaker then stays optimistic / fail-open).
func (c *Connection) NATBehaviour() (natMap, natFilter uint32) {
	if m := c.natMapV.Load(); m != 0 {
		return m, c.natFilterV.Load()
	}
	if f := c.natFilterV.Load(); f != 0 {
		return 0, f
	}
	c.stationMu.RLock()
	defer c.stationMu.RUnlock()
	for _, u := range c.stationURLs {
		if u == nil {
			continue
		}
		nm, nf := u.GetInt("natm"), u.GetInt("natf")
		if nm != 0 || nf != 0 {
			return uint32(nm), uint32(nf)
		}
	}
	return 0, 0
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
	}
}

// Feed processes raw bytes from the transport (0+ complete packets, buffering a
// trailing partial packet until the rest arrives).
func (c *Connection) Feed(data []byte) {
	c.recvMu.Lock()
	c.recvBuf = append(c.recvBuf, data...)
	packets, rest, err := DecodePackets(c.recvBuf)
	if err != nil {
		// Framing is corrupt; drop the buffer and keep the connection so a
		// following clean packet can recover.
		c.recvBuf = nil
		c.recvMu.Unlock()
		return
	}
	c.recvBuf = rest
	c.recvMu.Unlock()
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
		// Port secure : aucune DATA avant un CONNECT accepte (ticket valide). Sans cette
		// garde, une socket qui n'a jamais fait de CONNECT faisait executer n'importe quel
		// gestionnaire RMC sous le PID 0 (dont RequestProbeInitiationExt vers n'importe
		// quelle connexion). Une console legitime n'envoie de DATA qu'apres le CONNECT-ACK :
		// prod SSBU du 15/10 21:59 au 16/10 08:39, 0 requete RMC journalisee sous pid=0 et 0
		// CONNECT refuse. Une connexion fermee (reaper, kick) apres un CONNECT accepte n'est
		// pas concernee : servie comme avant. Une connexion fermee SANS CONNECT accepte
		// (DISCONNECT ou socket fermee avant) reste refusee (connecteUneFois). Le port
		// d'authentification (non secure) n'est pas concerne.
		if c.Endpoint.Secure && !c.connecteUneFois.Load() {
			if c.journalAvantConnect.CompareAndSwap(false, true) {
				fmt.Printf("[PRUDP] DATA avant CONNECT depuis %s : ignoree (port secure)\n", c.RemoteAddr)
			}

			return
		}
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
			return
		}
		// DERNIER ANGLE MORT DE LA PILE. Un type de paquet sans gestionnaire etait
		// abandonne sans un mot — comme l'etait le RMC illisible juste au-dessus. Si la
		// console parle et qu'on ignore, le symptome est identique a celui d'une console
		// muette : elle attend, puis affiche une erreur de connexion.
		//
		// On l'annonce UNE FOIS PAR TYPE. Ces paquets peuvent arriver toutes les
		// secondes ; une ligne a chaque fois noierait le journal et rendrait le reste
		// illisible, ce qui reviendrait a ne rien journaliser du tout.
		typesInconnusMu.Lock()
		dejaVu := typesInconnus[p.Type]
		typesInconnus[p.Type] = true
		typesInconnusMu.Unlock()
		if !dejaVu {
			fmt.Printf("[PRUDP] type de paquet %d SANS GESTIONNAIRE (ignore) — charge=%do, drapeaux=0x%x\n",
				p.Type, len(p.Payload), p.Flags)
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
		// HasSize sur le SYN-ACK, comme sur le CONNECT-ACK ; le SYN-ACK de Nintendo ne le porte
		// pas (drapeaux 0x001).
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

	// Un seul CONNECT accepte par connexion. Avant, chaque CONNECT valide recu sur une
	// connexion deja connectee la re-enregistrait sous un NOUVEL identifiant, relancait une
	// goroutine retransmitLoop et rappelait OnConnect ; close() ne retirait que le dernier
	// identifiant : les autres restaient dans e.connections pour toujours (revue t4 :
	// 6393 CONNECT sur une socket = 6393 entrees et goroutines, 406 Mio en moins d'une
	// seconde par le vrai transport, au-dela de la memoire du conteneur). En prod, des consoles
	// envoient deja leur CONNECT en double sur la meme socket (4 adresses, a moins de 1,3 s,
	// ou dans les memes 40 microsecondes) : la console attend une reponse, on la lui rend.
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	if c.state.Load() == stateClosed {
		// Connexion fermee (DISCONNECT, socket fermee, reaper) : un CONNECT encore en file
		// (gws) ne la ressuscite pas.
		c.journalConnectRefuse("connexion fermee")
		return
	}
	if c.connecteUneFois.Load() {
		c.connectRejoue(p)
		return
	}

	login, err := c.processLoginRequest(p.Payload)
	if err != nil {
		fmt.Printf("[PRUDP] CONNECT login failed from %s: %v (payload=%d bytes)\n", c.RemoteAddr, err, len(p.Payload))
		return // bad ticket — ignore
	}
	enc := c.connectAckDe(login.response)

	// Entree sous cycleMu : enregistrement, retransmitLoop et OnConnect AVANT l'envoi du
	// CONNECT-ACK (la premiere DATA de la console, qui suit cet ACK, trouve la connexion
	// connectee et deja vue par OnConnect). Si close() est passe entre-temps, rien.
	if !c.entrerConnecte(login, p.Payload, enc) {
		c.journalConnectRefuse("connexion fermee pendant le CONNECT")
		return
	}

	c.send(enc)
}

// entrerConnecte (sous connectMu) : passage a « connectee », enregistrement, retransmitLoop et
// OnConnect, sous cycleMu rendu par defer (une panique dans OnConnect ne le garde pas pris :
// close() attendrait sinon pour toujours). false : close() est passe avant, rien n'est fait.
func (c *Connection) entrerConnecte(login loginConnect, payload, enc []byte) bool {
	c.cycleMu.Lock()
	defer c.cycleMu.Unlock()
	if !c.state.CompareAndSwap(stateConnecting, stateConnected) {
		return false
	}
	if login.secure {
		c.PID, c.CID, c.sessionKey = login.pid, login.cid, login.sessionKey
	}
	c.SansTicket = login.sansTicket
	c.connectPayload = append([]byte(nil), payload...)
	c.connectAck = enc
	c.connecteUneFois.Store(true)
	fmt.Printf("[PRUDP] CONNECT ok from %s pid=%d secure=%v\n", c.RemoteAddr, c.PID, c.Endpoint.Secure)
	c.Endpoint.registerConnection(c)
	go c.retransmitLoop() // re-send unacked reliable packets (large-payload reliability)
	if c.Endpoint.OnConnect != nil {
		c.Endpoint.OnConnect(c)
	}
	return true
}

// connectAckDe encode le CONNECT-ACK portant la reponse de connexion.
func (c *Connection) connectAckDe(response []byte) []byte {
	return EncodePacket(&Packet{
		Type:       PacketCONNECT,
		Flags:      FlagACK | FlagHasSize,
		SourceType: c.srcType, SourcePort: c.srcPort,
		DestType: c.dstType, DestPort: c.dstPort,
		MinorVersion:  c.minorVersion,
		SupportedFunc: c.supportedFunc,
		PacketID:      1,
		Payload:       response,
	})
}

// connectRejoue (sous connectMu) : CONNECT recu sur une connexion deja connectee. Meme
// charge utile (retransmission de la console) : le MEME CONNECT-ACK. Autre charge utile :
// verifiee comme un premier CONNECT ; si son ticket prouve le MEME PID, un CONNECT-ACK
// portant sa reponse (comme avant pour la console), sinon rien. Dans les deux cas, aucun
// nouvel enregistrement, aucune goroutine, aucun OnConnect, et le PID ne change pas.
func (c *Connection) connectRejoue(p *Packet) {
	if equalBytes(p.Payload, c.connectPayload) {
		c.send(c.connectAck)
		return
	}
	login, err := c.processLoginRequest(p.Payload)
	if err != nil || (login.secure && login.pid != c.PID) {
		c.journalConnectRefuse(fmt.Sprintf("CONNECT rejoue avec un autre ticket (pid %d, erreur %v)", login.pid, err))
		return
	}
	c.send(c.connectAckDe(login.response))
}

func (c *Connection) journalConnectRefuse(pourquoi string) {
	if c.journalConnect.CompareAndSwap(false, true) {
		fmt.Printf("[PRUDP] CONNECT ignore depuis %s pid=%d : %s\n", c.RemoteAddr, c.PID, pourquoi)
	}
}

// loginConnect : ce que prouve un CONNECT (rien n'est ecrit dans la connexion).
type loginConnect struct {
	response   []byte
	secure     bool
	pid        uint64
	cid        uint32
	sessionKey []byte
	// sansTicket : l'identite ne vient pas d'un ticket (verificateur de CONNECT).
	sansTicket bool
}

// processLoginRequest valide la charge utile d'un CONNECT. Endpoint non secure (auth) : rien
// a verifier. Endpoint secure : l'identite est etablie par le verificateur de CONNECT, qui
// n'est pas inclus dans la version publique du coeur (verification_publique.go) ; sans
// verificateur installe, le CONNECT est refuse.
func (c *Connection) processLoginRequest(payload []byte) (loginConnect, error) {
	if !c.Endpoint.Secure {
		return loginConnect{response: []byte{}}, nil
	}
	reponse, id, err := verifierConnexion(c, payload)
	if err != nil {
		return loginConnect{}, err
	}
	return loginConnect{response: reponse, secure: true, pid: id.PID, cid: id.CID, sessionKey: id.CleSession, sansTicket: !id.Ticket}, nil
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
	c.fragMu.Lock()
	// DATA deja recue : la console la reemet quand l'ACK lui manque, avec le MEME numero
	// de sequence. Elle vient d'etre acquittee de nouveau (plus haut), on ne la sert pas une
	// seconde fois. Prod SSBU du 17/10 00:30Z a 03:40Z : 194 requetes servies deux fois sur
	// la meme connexion (54 joueurs), dont des AutoMatchmake qui ouvraient un second salon
	// fantome, et des fragments finaux reemis reassembles seuls (« size mismatch » avec un
	// en-tete en texte ASCII).
	if c.seqDejaRecu(p) {
		c.fragMu.Unlock()

		return
	}
	// Requete trop grosse (MaxRMCSize) : ses fragments sont acquittes (plus haut) et jetes
	// jusqu'au dernier, sans rien garder ; la requete suivante repart de zero.
	if limite := c.Endpoint.MaxRMCSize; limite > 0 && (c.fragRejet || len(c.fragBuf)+len(p.Payload) > limite) {
		dejaRejete := c.fragRejet
		c.fragRejet = p.FragmentID != 0
		c.fragBuf = nil
		c.fragMu.Unlock()
		if !dejaRejete {
			fmt.Printf("[PRUDP] pid=%d addr=%s requete RMC de plus de %d octets : jetee\n", c.PID, c.RemoteAddr, limite)
		}

		return
	}
	// Reassemble reliable fragments in arrival order (WebSocket is in-order).
	c.fragBuf = append(c.fragBuf, p.Payload...)
	var full []byte
	complet := p.FragmentID == 0
	if complet {
		full = c.fragBuf
		c.fragBuf = nil
	}
	c.fragMu.Unlock()
	if complet {
		c.lancerRMC(full)
	}
}

// seqDejaRecu (sous fragMu) dit si cette DATA fiable a deja ete recue, et la note sinon.
//
// Une case par PacketID%256, et non une fenetre monotone : les messages d'une socket
// peuvent etre traites en parallele (TestFeedEnParalleleSurUneSocket), un numero plus petit
// qu'un autre deja vu n'est pas pour autant un doublon. Une reemission arrive bien avant 256
// DATA plus recentes (prod : moins de 2 s apres l'original) ; au-dela, elle serait servie
// comme avant ce correctif. Les DATA non fiables ne sont pas concernees.
func (c *Connection) seqDejaRecu(p *Packet) bool {
	if !p.HasFlag(FlagReliable) {
		return false
	}
	marque := uint32(p.PacketID) + 1
	if c.seqRecus[p.PacketID%256] == marque {
		return true
	}
	c.seqRecus[p.PacketID%256] = marque

	return false
}

// lancerRMC sert une requete reassemblee. Avec le transport WebSocket (rmcEnVol pose), dans
// sa propre goroutine : la lecture des messages suivants de la socket (ACK, PING, autres
// requetes) ne depend pas d'un gestionnaire lent, et une panique d'un gestionnaire est
// recuperee et journalisee (avant, par gws.Recovery) au lieu d'arreter le serveur.
func (c *Connection) lancerRMC(full []byte) {
	if c.rmcEnVol == nil {
		c.dispatchRMC(full)
		return
	}
	c.rmcEnVol <- struct{}{}
	go func() {
		defer func() { <-c.rmcEnVol }()
		defer func() {
			if v := recover(); v != nil {
				fmt.Printf("[RMC] PANIQUE recuperee (pid=%d addr=%s) : %v\n%s\n", c.PID, c.RemoteAddr, v, debug.Stack())
			}
		}()
		c.dispatchRMC(full)
	}()
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
	if err == nil && req.Mode == RMCResponse {
		// Reponse de la console a une requete que le SERVEUR lui a poussee (InitiateProbe,
		// nattraversal.go : call 0xFFFF0000 + call de l'appelant). Personne n'attend rien :
		// ce n'est pas une requete abandonnee. Prod SSBU du 17/10 : 18 reponses d'erreur
		// 0x80010002 a un InitiateProbe, journalisees jusque-la « requete ABANDONNEE (mode
		// inattendu) ».
		fmt.Printf("[RMC] reponse de pid=%d au push 0x%02x call=%#x : erreur=%v code=%#x\n",
			c.PID, req.Protocol, req.CallID, req.IsError, req.Result)

		return
	}
	if err != nil || req.Mode != RMCRequest {
		// CE RETOUR ETAIT MUET, et c'est le pire endroit du serveur pour l'etre : une
		// requete qu'on ne sait pas analyser est abandonnee SANS reponse, donc la console
		// attend jusqu'a expiration. A l'ecran cela donne « erreur de connexion » apres
		// quelques secondes ; dans le journal, rien du tout — pas meme la ligne
		// [Secure] proto=…, puisque le crochet OnRMC ne s'execute qu'apres cette analyse.
		//
		// C'est exactement la forme du blocage du multijoueur de SMM2 : le jeu se
		// connecte, se prepare sans erreur, puis n'envoie plus rien de visible. On ne
		// pouvait pas savoir s'il se taisait ou si on l'ignorait.
		//
		// On journalise donc, avec le debut de la charge : le protocole et la methode
		// sont dans ses premiers octets, et c'est tout ce qu'il faut pour savoir quoi
		// implementer.
		n := len(payload)
		if n > 64 {
			n = 64
		}
		mode := "mode inattendu"
		if err != nil {
			mode = "analyse impossible: " + err.Error()
		}
		fmt.Printf("[RMC] requete ABANDONNEE (%s) len=%d debut=%x\n", mode, len(payload), payload[:n])
		return
	}
	if c.Endpoint.OnRMC != nil {
		c.Endpoint.OnRMC(c, req)
	}
	handler, ok := c.Endpoint.handler(req.Protocol)
	if !ok {
		// Ici au moins une reponse part, donc la console n'attend pas — mais elle recoit
		// NotImplemented sans qu'on sache lequel. On le dit.
		fmt.Printf("[RMC] protocole 0x%02x NON ENREGISTRE (methode %d, call %d) -> NotImplemented\n",
			req.Protocol, req.Method, req.CallID)
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
			if c.state.Load() == stateClosed {
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
			// the previous stack measured blasts all 13 fragments of SSBU's 15.7 KB DataStore init in
			// ~200 ms at a steady ~16 ms/fragment, in strict order, WITHOUT stopping to
			// wait for acks. Ack-driven windowing here instead took ~2.2 s, which tripped
			// SSBU's native DataStore-fetch watchdog and crashed the emulator (the small
			// single-fragment arena responses were unaffected, which is why arenas worked
			// but quick match didn't). retransmitLoop still backstops any dropped fragment.
			if fragmentPacing > 0 {
				time.Sleep(fragmentPacing)
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
		// but never accepts the message. Match the measured exactly.
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
		if c.state.Load() == stateClosed {
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

		// DIAGNOSTIC. Un paquet fiable que la console n'acquitte JAMAIS reste ici et se
		// reemet indefiniment : le compteur ne redescend pas. C'est ainsi qu'on distingue
		// « la console n'a pas recu » de « la console a recu et a ignore » — question
		// ouverte pour les notifications de PAC-MAN 99, qu'il ne semble pas traiter.
		if notifDiagRetransmit {
			fmt.Printf("[PRUDP diag] pid=%d : %d paquet(s) fiable(s) non acquitte(s), reemission\n",
				c.PID, len(batch))
		}
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
	// cycleMu : un CONNECT en cours d'entree (enregistrement, OnConnect) se termine avant ;
	// OnDisconnect ne part donc jamais avant OnConnect, et l'identifiant enregistre est
	// bien celui qui est retire. Jamais connectMu (voir la structure).
	c.cycleMu.Lock()
	defer c.cycleMu.Unlock()
	// Swap : une seule fermeture effective meme si le DISCONNECT et la fermeture de la
	// socket arrivent en meme temps (avant : OnDisconnect pouvait partir deux fois).
	if c.state.Swap(stateClosed) == stateClosed {
		return
	}
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

// typesInconnus retient les types de paquets deja signales, pour n'en parler qu'une fois.
var (
	typesInconnusMu sync.Mutex
	typesInconnus   = map[uint8]bool{}
)
