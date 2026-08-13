package nex

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
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

// authPIDByIP remembers the NEX PID minted at LoginEx, keyed by the client's IP.
// The secure connection currently arrives WITHOUT a decryptable ticket (private-test
// leniency) and must reuse that same PID — otherwise the session it hosts is owned
// by a placeholder PID the console doesn't recognise as itself, and Pia gives up with
// 2618-562 SessionKeepFailed (the console waits forever for a "host" that isn't it).
var authPIDByIP sync.Map // ip host -> uint64

func ipHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// authRecall is a login PID remembered for ONE client IP, with the instant it was minted.
// It expires: an IP→PID association that lives forever would let whoever holds that address
// later (CGNAT/DHCP reuse, a peer behind the same public IP) inherit a stranger's identity on
// a ticketless secure CONNECT.
type authRecall struct {
	pid uint64
	at  time.Time
}

// authRecallTTL bounds how long a login PID may be inherited by a ticketless secure
// CONNECT from the SAME IP. It must cover a whole play session: a console re-CONNECTs to
// the secure server repeatedly (lobby changes, reconnects) without necessarily replaying
// the full auth, so a short window would refuse legitimate players mid-session.
const authRecallTTL = 24 * time.Hour

// Persistence of the IP→PID recall.
//
// The table lives in memory, so a server RESTART emptied it: every console already in a
// session that re-CONNECTed to the secure port without replaying a full login was then
// refused (measured on MK8: 40 refusals right after a restart, players stuck at 30/50).
// The old code papered over that with the IP-agnostic fallback we removed for security, so
// the recall must instead SURVIVE restarts. Writes are debounced off the request path;
// the file is re-read at startup. Empty NEXTENDO_AUTH_RECALL_FILE = memory only.
var (
	authRecallFile  = os.Getenv("NEXTENDO_AUTH_RECALL_FILE")
	authRecallOnce  sync.Once
	authRecallDirty atomic.Bool
)

const (
	authRecallFlushEvery = 5 * time.Second
	authRecallMax        = 50000 // borne dure : une table de rappel ne doit pas croître sans fin
)

// authRecallPersisted is the on-disk shape (JSON: ip -> {pid, unix seconds}).
type authRecallPersisted struct {
	PID uint64 `json:"pid"`
	At  int64  `json:"at"`
}

// authRecallInit loads the persisted recall table once and starts the flusher.
func authRecallInit() {
	authRecallOnce.Do(func() {
		if authRecallFile == "" {
			return
		}
		b, err := os.ReadFile(authRecallFile)
		if err == nil {
			var m map[string]authRecallPersisted
			if json.Unmarshal(b, &m) == nil {
				n := 0
				for ip, rec := range m {
					at := time.Unix(rec.At, 0)
					if rec.PID == 0 || time.Since(at) > authRecallTTL {
						continue
					}
					authPIDByIP.Store(ip, authRecall{pid: rec.PID, at: at})
					n++
				}
				fmt.Printf("[Auth] rappel IP→PID restauré : %d entrée(s) depuis %s\n", n, authRecallFile)
			}
		}
		go authRecallFlusher()
	})
}

// authRecallFlusher persists the table at most once per authRecallFlushEvery, dropping
// expired entries. Never runs on the request path.
func authRecallFlusher() {
	t := time.NewTicker(authRecallFlushEvery)
	defer t.Stop()
	for range t.C {
		if !authRecallDirty.Swap(false) {
			continue
		}
		out := map[string]authRecallPersisted{}
		authPIDByIP.Range(func(k, v any) bool {
			ip, _ := k.(string)
			rec, ok := v.(authRecall)
			if !ok || ip == "" {
				return true
			}
			if time.Since(rec.at) > authRecallTTL {
				authPIDByIP.Delete(ip) // expirée : on la retire au passage
				return true
			}
			if len(out) < authRecallMax {
				out[ip] = authRecallPersisted{PID: rec.pid, At: rec.at.Unix()}
			}
			return true
		})
		b, err := json.Marshal(out)
		if err != nil {
			continue
		}
		tmp := authRecallFile + ".tmp"
		if os.WriteFile(tmp, b, 0600) == nil { // 0600 : associations IP→joueur
			_ = os.Rename(tmp, authRecallFile) // remplacement atomique
		}
	}
}

// RememberAuthPID records the login PID for a client address (called from LoginEx).
func RememberAuthPID(addr string, pid uint64) {
	authRecallInit()
	authPIDByIP.Store(ipHost(addr), authRecall{pid: pid, at: time.Now()})
	authRecallDirty.Store(true)
}

// RecallAuthPID returns the login PID recently minted for THIS client address.
//
// SECURITY: this is the only identity a ticketless secure CONNECT may inherit, and only from
// the very IP that authenticated, within authRecallTTL. The former IP-agnostic fallback
// (“the most recent login PID minted by ANYONE in the last 8s”) was removed: it let anyone
// who could reach the secure port — the PRUDP signature only proves knowledge of the PUBLIC
// access key, not identity — take over the account of whoever had just logged in, and via the
// one-place-online gate evict the real owner. Measured before removal: 8 uses fleet-wide vs
// 243 legitimate same-IP recalls, so dropping it costs nothing real.
func RecallAuthPID(addr string) (uint64, bool) {
	authRecallInit() // le CONNECT securise peut arriver avant tout login sur ce processus
	v, ok := authPIDByIP.Load(ipHost(addr))
	if !ok {
		return 0, false
	}
	rec, ok := v.(authRecall)
	if !ok || rec.pid == 0 {
		return 0, false
	}
	if time.Since(rec.at) > authRecallTTL {
		authPIDByIP.Delete(ipHost(addr))
		return 0, false
	}
	return rec.pid, true
}

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
// Username (the account PID string), ExtraData (BAAS JWT DataHolder), ...}; we only need
// the Username to resolve the account (+ apply the gates) and issue the ticket.
func (cfg *AuthConfig) handleLoginWithContext(conn *Connection, req *RMCMessage) *RMCMessage {
	s := cfg.Settings
	in := NewStreamIn(req.Body, s)
	_ = in.U8()  // structure header: version
	_ = in.U32() // structure header: content length
	_ = in.U32() // PlatformType
	username := in.String()
	// ExtraData follows the username (an AuthenticationInfo DataHolder carrying the
	// BAAS id_token). Previously discarded; we now forward it so ResolveUser can verify
	// the cryptographic account binding (the nx2 token in the id_token's "nnex" claim).
	extraData := in.ReadAll()
	fmt.Printf("[Auth] ValidateAndRequestTicketWithParam username=%q extraDataLen=%d\n", username, len(extraData))

	pid, sourceKey, ok := cfg.ResolveUser(username, extraData)
	if !ok {
		return NewRMCError(s, ProtocolTicketGranting, req.CallID, ResultAuthTokenParseError)
	}
	if conn != nil {
		RememberAuthPID(conn.RemoteAddr, pid)
	}

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

	pid, sourceKey, ok := cfg.ResolveUser(username, extraData)
	if !ok {
		return fail(ResultAuthTokenParseError)
	}

	// Remember this PID so the ticket-less secure connection from the same client
	// inherits it instead of a placeholder (keeps session ownership = the console).
	if conn != nil {
		RememberAuthPID(conn.RemoteAddr, pid)
	}

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
