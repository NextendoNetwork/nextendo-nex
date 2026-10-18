package nex

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// TicketGranting protocol (the auth server).
const (
	ProtocolTicketGranting uint16 = 0x0A
	MethodLogin            uint32 = 1
	MethodLoginEx          uint32 = 2
	MethodRequestTicket    uint32 = 3
	// MethodLoginWithContext (6) is called by SSBU at BOOT (MK8 doesn't). If it never
	// gets a valid answer SSBU stalls at the loading spinner ("balle Smash"). We log
	// the request to learn its format, then answer like a normal login.
	MethodLoginWithContext uint32 = 6
)

// L'identite d'un CONNECT secure est etablie par le verificateur de CONNECT
// (verification_publique.go).

// RVConnectionData tells the client where to reach the secure server. NEX >= 3.5
// uses version 1, which appends the server time.
type RVConnectionData struct {
	MainStation      *StationURL
	SpecialProtocols []uint8
	SpecialStation   *StationURL
	ServerTime       DateTime
}

// Levels implements Structure.
func (d *RVConnectionData) Levels() []Level {
	return []Level{{
		Version: 1,
		Save: func(o *StreamOut) {
			o.StationURL(orEmptyStation(d.MainStation))
			WriteList(o, d.SpecialProtocols, func(o *StreamOut, v uint8) { o.U8(v) })
			o.StationURL(orEmptyStation(d.SpecialStation))
			o.DateTime(d.ServerTime.Value())
		},
		Load: func(i *StreamIn) {
			d.MainStation = i.StationURLValue()
			d.SpecialProtocols = ReadList(i, func(i *StreamIn) uint8 { return i.U8() })
			d.SpecialStation = i.StationURLValue()
			d.ServerTime = DateTime(i.DateTime())
		},
	}}
}

func orEmptyStation(u *StationURL) *StationURL {
	if u == nil {
		return NewStationURL("prudp")
	}
	return u
}

// AuthConfig configures the ticket-granting (auth) handler. The auth endpoint is
// insecure; this handler answers LoginEx by minting a Kerberos ticket the client
// uses to connect to the (secure) game server.
type AuthConfig struct {
	Settings *Settings

	// SecurePID / SecurePassword identify the secure server account; its derived
	// key encrypts the ticket's internal data (and MUST match the secure endpoint).
	SecurePID      uint64
	SecurePassword string

	// SecureStationURL is advertised to the client as the secure server address.
	SecureStationURL *StationURL
	SpecialProtocols []uint8
	ServerName       string
	SessionKeyLength int

	// ResolveUser maps a login username (+ raw extra data) to an account PID and
	// the source key that encrypts the client ticket (returned to the client as
	// pSourceKey, so the client can decrypt without a shared password). Return
	// ok=false to reject the login.
	ResolveUser func(username string, extraData []byte) (pid uint64, sourceKey []byte, ok bool)

	// ResolveUserEx, when set, replaces ResolveUser and lets the caller pick the result
	// code a rejection returns. A zero result means success.
	ResolveUserEx func(username string, extraData []byte) (pid uint64, sourceKey []byte, result uint32)
}

func (cfg *AuthConfig) resolve(username string, extraData []byte) (uint64, []byte, uint32) {
	if cfg.ResolveUserEx != nil {
		return cfg.ResolveUserEx(username, extraData)
	}
	if cfg.ResolveUser == nil {
		return 0, nil, ResultAuthTokenParseError // aucun résolveur : refus
	}
	pid, sourceKey, ok := cfg.ResolveUser(username, extraData)
	if !ok {
		return 0, nil, ResultAuthTokenParseError
	}
	return pid, sourceKey, 0
}

// Handler returns the RMC handler for the TicketGranting protocol (0x0A).
func (cfg *AuthConfig) Handler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodLogin, MethodLoginEx:
			return cfg.handleLogin(conn, req)
		case MethodLoginWithContext:
			return cfg.handleLoginWithContext(conn, req)
		default:
			return NewRMCError(cfg.Settings, ProtocolTicketGranting, req.CallID, ResultCoreNotImplemented)
		}
	}
}

// handleLoginWithContext answers TicketGranting method 0x6, which the Switch dispatches
// as ValidateAndRequestTicketWithParam (NEX 4.6+ titles like SSBU use it instead of
// LoginEx 0x2). CRITICAL: method 0x6 expects the ValidateAndRequestTicketResult
// STRUCTURE {SourcePID, BufResponse(ticket), ServiceNodeURL, CurrentUTCTime, ReturnMsg,
// SourceKey} wrapped in a struct header (version 1) — NOT the flat LoginEx layout.
// Returning the LoginEx layout makes the client reject it with Core::InvalidArgument
// (2306-0116). Request = a ValidateAndRequestTicketParam struct {header, PlatformType,
// Username (the account PID string), ExtraData (AuthenticationInfo DataHolder), ...}; we only need
// the Username to resolve the account (+ apply the gates) and issue the ticket.
func (cfg *AuthConfig) handleLoginWithContext(conn *Connection, req *RMCMessage) *RMCMessage {
	s := cfg.Settings
	in := NewStreamIn(req.Body, s)
	_ = in.U8()  // structure header: version
	_ = in.U32() // structure header: content length
	_ = in.U32() // PlatformType
	username := in.String()
	// ExtraData follows the username (an AuthenticationInfo DataHolder); it is forwarded
	// to ResolveUser, which decides on the account.
	extraData := in.ReadAll()
	fmt.Printf("[Auth] ValidateAndRequestTicketWithParam username=%q extraDataLen=%d\n", username, len(extraData))

	pid, sourceKey, rc := cfg.resolve(username, extraData)
	if rc != 0 {
		return NewRMCError(s, ProtocolTicketGranting, req.CallID, rc)
	}
	// On retient AUSSI la correspondance identifiant-de-connexion -> PID.
	//
	// Certains jeux ne parlent jamais notre PID interne : Super Mario Maker 2 reclame
	// les profils par l'identifiant NSA, celui-la meme qui arrive ici comme `username`.
	// Sans cette table, DataStore cherchait un PID qu'on ne lui avait jamais donne et
	// ne trouvait jamais rien — le jeu retombait sur un Mii generique.
	RememberLoginName(username, pid)

	keyLen := cfg.SessionKeyLength
	if keyLen == 0 {
		keyLen = s.KerberosKeySize
	}
	sessionKey := make([]byte, keyLen)
	if _, err := rand.Read(sessionKey); err != nil {
		return NewRMCError(s, ProtocolTicketGranting, req.CallID, ResultAuthUnknown)
	}
	targetKey := s.DeriveKey([]byte(cfg.SecurePassword), cfg.SecurePID)
	internal, err := (&ServerTicket{Timestamp: NowDateTime(), Source: pid, SessionKey: sessionKey}).Encrypt(targetKey, s)
	if err != nil {
		return NewRMCError(s, ProtocolTicketGranting, req.CallID, ResultAuthUnknown)
	}
	ticket, err := (&ClientTicket{SessionKey: sessionKey, Target: cfg.SecurePID, Internal: internal}).Encrypt(sourceKey, s)
	if err != nil {
		return NewRMCError(s, ProtocolTicketGranting, req.CallID, ResultAuthUnknown)
	}

	// Build the ValidateAndRequestTicketResult structure: struct header (version 1 +
	// content length) then the fields, byte-for-byte like the proven server.
	content := NewStreamOut(s)
	content.PID(pid)                                         // SourcePID
	content.Buffer(ticket)                                   // BufResponse
	content.StationURL(orEmptyStation(cfg.SecureStationURL)) // ServiceNodeURL
	content.DateTime(NowDateTime().Value())                  // CurrentUTCTime
	content.String(cfg.ServerName)                           // ReturnMsg
	content.String(hex.EncodeToString(sourceKey))            // SourceKey (client decrypts the ticket with it)
	body := content.Bytes()

	out := NewStreamOut(s)
	out.U8(1)                  // structure version (NEX >= 3.5.0)
	out.U32(uint32(len(body))) // structure content length
	out.Write(body)

	fmt.Printf("[Auth] ValidateAndRequestTicket resp: pid=%d ticketLen=%d srcKeyLen=%d respLen=%d\n", pid, len(ticket), len(sourceKey), len(out.Bytes()))
	return NewRMCSuccess(s, ProtocolTicketGranting, req.Method, req.CallID, out.Bytes())
}

func (cfg *AuthConfig) handleLogin(conn *Connection, req *RMCMessage) *RMCMessage {
	s := cfg.Settings
	in := NewStreamIn(req.Body, s)
	username := in.String()
	extraData := in.ReadAll()

	fail := func(code uint32) *RMCMessage {
		return cfg.buildResponse(req, code|ResultErrorMask, 0, nil, nil)
	}

	fmt.Printf("[Auth] LoginEx username=%q (len=%d) extraDataLen=%d\n", username, len(username), len(extraData))

	pid, sourceKey, rc := cfg.resolve(username, extraData)
	if rc != 0 {
		return fail(rc)
	}

	// On retient AUSSI la correspondance identifiant-de-connexion -> PID.
	//
	// Certains jeux ne parlent jamais notre PID interne : Super Mario Maker 2 reclame
	// les profils par l'identifiant NSA, celui-la meme qui arrive ici comme `username`.
	// Sans cette table, DataStore cherchait un PID qu'on ne lui avait jamais donne et
	// ne trouvait jamais rien — le jeu retombait sur un Mii generique.
	RememberLoginName(username, pid)

	keyLen := cfg.SessionKeyLength
	if keyLen == 0 {
		keyLen = s.KerberosKeySize
	}
	sessionKey := make([]byte, keyLen)
	if _, err := rand.Read(sessionKey); err != nil {
		return fail(ResultAuthUnknown)
	}

	// The ticket's internal data is encrypted with the secure server's key so
	// only the secure server can read the session key + source PID.
	targetKey := s.DeriveKey([]byte(cfg.SecurePassword), cfg.SecurePID)
	internal, err := (&ServerTicket{Timestamp: NowDateTime(), Source: pid, SessionKey: sessionKey}).Encrypt(targetKey, s)
	if err != nil {
		return fail(ResultAuthUnknown)
	}

	// The outer ticket is encrypted with the source key, which we hand back to
	// the client as pSourceKey below.
	ticket, err := (&ClientTicket{SessionKey: sessionKey, Target: cfg.SecurePID, Internal: internal}).Encrypt(sourceKey, s)
	if err != nil {
		return fail(ResultAuthUnknown)
	}

	return cfg.buildResponse(req, ResultCoreUnknown, pid, ticket, sourceKey)
}

func (cfg *AuthConfig) buildResponse(req *RMCMessage, retval uint32, pid uint64, ticket, sourceKey []byte) *RMCMessage {
	s := cfg.Settings
	rvcd := &RVConnectionData{
		MainStation:      orEmptyStation(cfg.SecureStationURL),
		SpecialProtocols: cfg.SpecialProtocols,
		SpecialStation:   NewStationURL("prudp"),
		ServerTime:       NowDateTime(),
	}

	out := NewStreamOut(s)
	out.U32(retval)            // retval (QResult)
	out.PID(pid)               // pidPrincipal
	out.Buffer(ticket)         // pbufResponse (encrypted ticket)
	out.Add(rvcd)              // pConnectionData
	out.String(cfg.ServerName) // strReturnMsg
	if s.NexVersion >= 40000 {
		out.String(hex.EncodeToString(sourceKey)) // pSourceKey (NEX 4)
	}

	fmt.Printf("[Auth] LoginEx resp: retval=%#x pid=%d ticketLen=%d srcKey=%x respLen=%d\n  resp=%x\n",
		retval, pid, len(ticket), sourceKey, len(out.Bytes()), out.Bytes())

	return NewRMCSuccess(s, ProtocolTicketGranting, req.Method, req.CallID, out.Bytes())
}

// loginNameToPID relie l'identifiant de connexion (le NSA, pour la Switch) au PID du
// compte. Rempli a l'authentification, lu par les protocoles qui designent les joueurs
// par cet identifiant plutot que par notre PID interne.
var loginNameToPID sync.Map // string -> uint64

// RememberLoginName enregistre la correspondance nom de connexion -> PID.
func RememberLoginName(name string, pid uint64) {
	if name == "" || pid == 0 {
		return
	}
	loginNameToPID.Store(name, pid)
}

// PIDForLoginName rend le PID associe a un identifiant de connexion.
func PIDForLoginName(name string) (uint64, bool) {
	v, ok := loginNameToPID.Load(name)
	if !ok {
		return 0, false
	}
	return v.(uint64), true
}
