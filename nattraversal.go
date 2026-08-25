package nex

import (
	"fmt"
	"net"
	"os"
)

// probeRepointEnabled reports whether pushInitiateProbe must rewrite the station it
// pushes to the endpoint the server observes for the caller. Off unless the game
// server sets NEXTENDO_PROBE_REPOINT=1, so titles that already work are untouched.
func probeRepointEnabled() bool {
	v := os.Getenv("NEXTENDO_PROBE_REPOINT")
	return v == "1" || v == "true"
}

// splitHostPortSafe is net.SplitHostPort, tolerating an address with no port.
func splitHostPortSafe(addr string) (string, string, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, "", nil
	}
	return h, p, nil
}

// NAT traversal protocol (P2P hole-punch coordination).
const (
	ProtocolNATTraversal uint16 = 0x03

	MethodRequestProbeInitiation         uint32 = 0x1
	MethodInitiateProbe                  uint32 = 0x2
	MethodRequestProbeInitiationExt      uint32 = 0x3
	MethodReportNATTraversalResult       uint32 = 0x4
	MethodReportNATProperties            uint32 = 0x5
	MethodGetRelaySignatureKey           uint32 = 0x6
	MethodReportNATTraversalResultDetail uint32 = 0x7
)

// NATTraversalHandler coordinates the P2P hole-punch. The key method is
// RequestProbeInitiationExt: when a client asks the server to have its peers
// probe it back, we push an InitiateProbe request to each target carrying the
// caller's own station URL, so BOTH ends fire UDP probes and open their NATs at
// the same time. The stock flow opens only the joiner's NAT, leaving the other
// console's Pia waiting ("communication error"). Every other method is a simple
// acknowledgement (the actual hole-punch happens directly between the consoles).
func NATTraversalHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodRequestProbeInitiationExt:
			pushInitiateProbe(conn, req)
			return natAck(conn, req)
		case MethodReportNATTraversalResult, MethodReportNATTraversalResultDetail:
			logNATResult(conn, req)

			return natAck(conn, req)
		case MethodReportNATProperties:
			noteNATProperties(conn, req)
			return natAck(conn, req)
		case MethodGetRelaySignatureKey:
			return relaySignatureKey(conn, req)
		case MethodRequestProbeInitiation,
			MethodInitiateProbe:
			return natAck(conn, req)
		default:
			return notImplemented(conn, ProtocolNATTraversal, req)
		}
	}
}

func natAck(conn *Connection, req *RMCMessage) *RMCMessage {
	return NewRMCSuccess(conn.Settings, ProtocolNATTraversal, req.Method, req.CallID, nil)
}

// relaySignatureKey answers GetRelaySignatureKey — the relay handshake the NEWER Pia (Mario
// Strikers, with its RelayMesh stack) performs before starting the match, which MK8/S2/SSBU's older
// direct-mesh Pia never calls. NOT a relay server: we advertise NO relay (empty address, port 0) —
// exactly what the previous stack's server returns — so the game gets a well-formed "no relay offered" and
// proceeds on its DIRECT mesh (which the packet measured proved works bidirectionally). Left
// unhandled it fell through to a malformed empty-list the game cannot parse -> its match-start
// handshake never completes -> the match connects but never renders (the black screen). Response
// layout: [relayMode i32][currentUTCTime DateTime][relay address String][port u16][addressType i32]
// [gameServerID u32].
func relaySignatureKey(conn *Connection, req *RMCMessage) *RMCMessage {
	out := NewStreamOut(conn.Settings)
	out.U32(0)                          // relayMode (int32)
	out.DateTime(NowDateTime().Value()) // currentUTCTime (UTC)
	out.String("")                      // relay server address — empty = no relay offered
	out.U16(0)                          // relay server port
	out.U32(0)                          // relayAddressType (int32)
	out.U32(0)                          // gameServerID
	fmt.Printf("[NAT] pid=%d GetRelaySignatureKey -> no relay advertised (proceed on direct mesh)\n", conn.PID)
	return NewRMCSuccess(conn.Settings, ProtocolNATTraversal, req.Method, req.CallID, out.Bytes())
}

// pushInitiateProbe relays a server→client InitiateProbe to every target the
// caller named, carrying the caller's own station URL so each target probes back.
func pushInitiateProbe(conn *Connection, req *RMCMessage) {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	targetList := ReadList(in, func(i *StreamIn) string { return i.String() })
	stationToProbe := in.String()
	if in.Err() != nil {
		return
	}

	// Optional repointing (NEXTENDO_PROBE_REPOINT=1), OFF by default.
	//
	// Some clients advertise, in this payload, a station whose ADDRESS is the game
	// server's own — the host they are talking to — carrying their NAT-checked port.
	// Pushed verbatim, the peer then fires its punch at the SERVER's address on a port
	// nothing listens on, so every probe dies (rtt=0) even though both NATs are
	// perfectly punchable. Repointing rewrites that address to the endpoint the server
	// actually observes for the caller (its real public IP, and the UDP port the NAT
	// responder confirmed), leaving every other parameter untouched.
	//
	// Left off for the titles whose clients already advertise their own public
	// endpoint (their probes work as-is); enabled per game server.
	if probeRepointEnabled() {
		if host, _, err := splitHostPortSafe(conn.RemoteAddr); err == nil && !isPrivateIP(host) {
			u := ParseStationURL(stationToProbe)
			if u.Get("address") != host {
				u.Set("address", host)
			}
			if p, ok := natPortForIP(host); ok {
				u.SetInt("port", p)
			}
			repointed := u.String()
			if repointed != stationToProbe {
				fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d: station repointée vers l'endpoint observé %s\n", conn.PID, repointed)
				stationToProbe = repointed
			}
		}
	}

	// The InitiateProbe request body is a single station URL (the caller's).
	probe := NewStreamOut(s)
	probe.StationURL(ParseStationURL(stationToProbe))
	probeBody := probe.Bytes()

	for _, raw := range targetList {
		rvcid := uint32(ParseStationURL(raw).GetInt("RVCID"))
		if rvcid == 0 {
			fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d: cible sans RVCID (%q) — IGNORÉE\n", conn.PID, raw)
			continue
		}
		target := conn.Endpoint.FindConnectionByID(rvcid)
		if target == nil {
			fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d: cible RVCID=%d INTROUVABLE — pas d'InitiateProbe\n", conn.PID, rvcid)
			continue
		}
		// Relais par paire : la station poussee a CETTE cible doit designer le port de
		// la paire (appelant, cible). C est per-cible et non global — chaque pair d un
		// salon a son propre port — donc la substitution vit ici, dans la boucle.
		station := stationToProbe
		corps := probeBody
		if PairRelayActive() {
			if rHost, rPort, ok := PairRelayFor(conn.PID, target.PID, PublicIPOf(conn), PublicIPOf(target)); ok {
				u := ParseStationURL(station)
				u.Set("address", rHost)
				u.SetInt("port", rPort)
				station = u.String()
				out := NewStreamOut(s)
				out.StationURL(u)
				corps = out.Bytes()
			}
		}
		// Server-initiated requests use a distinct call-id space.
		fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d -> InitiateProbe VERS pid=%d (id=%d rvcid=%d) station=%q\n",
			conn.PID, target.PID, target.ID, rvcid, station)
		target.SendRMC(NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+req.CallID, corps))
	}
}

// logNATResult records the console's own verdict on the hole-punch.
//
// This is the one place either console tells us whether P2P actually worked, and it was
// being acknowledged and thrown away. Everything else we can see — the session forms, the
// bridge substitutes the right port, the probe is requested — happens BEFORE the punch;
// when a lobby then loops back to matchmaking, this report is the only evidence of why.
// A failure here means the two consoles never reached each other, which is a different
// problem from anything the server answers wrongly.
//
// Wire (from the proven protocol): ReportNATTraversalResult = (u32 cid, bool result,
// u32 rtt); the Detail variant inserts an i32 detail before the rtt.
func logNATResult(conn *Connection, req *RMCMessage) {
	in := NewStreamIn(req.Body, conn.Settings)

	cid := in.U32()
	result := in.Bool()

	detail := int32(0)
	if req.Method == MethodReportNATTraversalResultDetail {
		detail = int32(in.U32())
	}
	rtt := in.U32()

	if in.Err() != nil {
		fmt.Printf("[NAT] pid=%d result report unparsable (% x)\n", conn.PID, req.Body)

		return
	}

	verdict := "FAILED"
	if result {
		verdict = "ok"
	}

	fmt.Printf("[NAT] pid=%d hole-punch to cid=%d %s (rtt=%dms detail=%d)\n",
		conn.PID, cid, verdict, rtt, detail)
}

// noteNATProperties parses ReportNATProperties (natMapping u32, natFiltering u32,
// rtt u32) and forwards the player's NAT behaviour + ping to the dashboard hook. The
// stock flow just acked and discarded it, which is why NAT type and ping went blank
// on the monitoring site once the games moved onto this core.
func noteNATProperties(conn *Connection, req *RMCMessage) {
	in := NewStreamIn(req.Body, conn.Settings)
	natMap := in.U32()
	natFilter := in.U32()
	rtt := in.U32()
	if in.Err() != nil {
		return
	}
	// Cache the NAT behaviour on the connection so NAT-aware matchmaking can read it for
	// ANY participant (a joiner reads the existing members' connections). This is separate
	// from the dashboard hook below, which may be nil.
	conn.SetNATProps(natMap, natFilter)
	if conn.Endpoint.OnNATProperties != nil {
		conn.Endpoint.OnNATProperties(conn.PID, natMap, natFilter, rtt)
	}
}
