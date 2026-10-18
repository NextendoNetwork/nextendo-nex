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
	if len(stationToProbe) > natStationMax {
		fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d: station de %d octets (plafond %d) : aucun InitiateProbe\n",
			conn.PID, len(stationToProbe), natStationMax)

		return
	}
	// Un vrai salon nomme quelques pairs (prod : 3 entrees au plus sur 5340 requetes). Une
	// liste plus longue que natCiblesMax n'est traitee que sur ses natCiblesMax premieres
	// entrees : sans borne, une seule requete de quelques Mo poussait des centaines de
	// milliers d'InitiateProbe (natpret.go, plafonds).
	if len(targetList) > natCiblesMax {
		fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d: %d cibles, seules les %d premieres sont traitees (plafond)\n",
			conn.PID, len(targetList), natCiblesMax)
		targetList = targetList[:natCiblesMax]
	}
	// La Pia de l'appelant sonde deja : s'il n'est pas pret, son ReplaceURL n'est plus
	// attendu, rien ne sert de lui retenir les InitiateProbe en retour (natpret.go).
	conn.natSonde()
	// La station que l'appelant fait sonder est la sienne : son natm sert quand aucun
	// ReplaceURL ne l'a encore donne (natmStation).
	if m := ParseStationURL(stationToProbe).GetInt("natm"); m != 0 {
		conn.natmSonde.Store(int32(m))
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
		// Seulement la paire (arrivant, hote) dont les URL de session ont ete repointees
		// (relayedFor, NoterSessionRelayee) : en 2 contre 2 un autre membre du salon a recu
		// l adresse de l arrivant par l hote (Pia), en direct ; lui faire sonder le relais
		// pendant que l arrivant le sonde en direct casserait le percage a coup sur.
		if PairRelayActive() && SessionRelayee(conn.PID, target.PID) {
			if rHost, rPort, ok := PairRelayFor(conn.PID, target.PID, PublicIPOf(conn), PublicIPOf(target)); ok {
				// Meme fonction que le cote visiteur : les deux moities doivent
				// produire exactement la meme forme, sinon elles derivent en silence.
				u := relayPointStation(ParseStationURL(station), rHost, rPort)
				station = u.String()
				out := NewStreamOut(s)
				out.StationURL(u)
				corps = out.Bytes()
			}
		}
		// Server-initiated requests use a distinct call-id space.
		msg := NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+req.CallID, corps)
		probeinit := ParseStationURL(station).GetInt("probeinit") == 1

		// L'appelant fait sonder la cible : il repond donc au sondage qu'elle lui avait
		// envoye (constat « hote muet », natpret.go).
		target.natReponseDe(conn.ID)

		// Cible pas prete (aucun ReplaceURL depuis sa derniere remise a zero) et dont le
		// ReplaceURL est attendu : l'envoi l'attend, 2,5 s au plus. Une cible prete, ou dont
		// le ReplaceURL n'est pas attendu, est servie comme avant.
		if target != conn && target.natRetenir(conn, msg, rvcid, station, probeinit) {
			continue
		}
		target.natEnvoyerProbe(conn, msg, rvcid, station, probeinit)
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
	// Tentative close : ni « hote muet » ni « abandon silencieux » pour elle.
	conn.natVerdict(cid)

	// Ce verdict porte sur le percage DIRECT vers ce pair. Si la paire est passee par le
	// relais, son candidat public a ete repointe : il n y a plus de direct, et le verdict est
	// FAILED par construction. L alimenter avec ca, c est rendre l eligibilite
	// auto-entretenue — mesure du 2026-08-31, de 14 eligibles sur 46 a 134 sur 151.
	//
	// Le cid est le RVCID du pair, donc son Connection.ID : meme resolution que
	// pushInitiateProbe. Pair deja parti (cas courant) : sa trace (connexionPartie, gardee
	// relayVuTTL apres la fermeture) donne son PID et son natm, donc la paire relayee et la
	// classe ; avant, une paire relayee dont l hote venait de partir comptait comme un echec
	// direct, et un echec 0515 tombait en classe « ? » (prod du 17/10 : 4 verdicts sur
	// 1 337). Sans trace non plus = on compte le verdict, parce que l immense majorite des
	// verdicts sont directs.
	//
	// La classe Pia du percage (classePia) est calculee avant de noter le verdict : un echec
	// de classe 0515 ou 0516 (NAT strict) suffit a rendre le joueur eligible au relais
	// (besoinSeuilStrict, relaybesoin.go). Pair introuvable = classe « ? » = seuil de deux.
	//
	// Un echec direct 0515 met aussi en cause le pair, cote natm 2 (NoteCauseStricte) : il ne
	// rapporte pas ce percage lui-meme.
	relaye := false
	classe := "?"
	var pairPID uint64
	if peer := conn.Endpoint.FindConnectionByID(cid); peer != nil {
		classe = classePia(conn, peer)
		pairPID = peer.PID
	} else if parti, ok := conn.Endpoint.connexionPartie(cid); ok {
		classe = classePiaDe(PublicIPOf(conn), parti.ip, natmStation(conn), parti.natm)
		pairPID = parti.pid
	}
	if pairPID != 0 {
		relaye = PaireRelayee(conn.PID, pairPID)
	}
	if !relaye {
		NotePercageClasse(conn.PID, result, classe)
		if !result && classe == "0515" {
			NoteCauseStricte(pairPID, conn.PID)
		}
	} else if classe == "0516" {
		// Joueur natm 2 qui joue par le relais : son eligibilite ne doit pas expirer
		// pendant qu'il s'en sert (ProlongerBesoin, rien n'est compte).
		ProlongerBesoin(conn.PID)
	}
	fmt.Printf("[NAT] pid=%d hole-punch to cid=%d %s (rtt=%dms detail=%d relaye=%v classe=%s)\n",
		conn.PID, cid, verdict, rtt, detail, relaye, classe)
}

// natmStation : le natm que la console annonce dans sa station la plus recente, celui que
// Pia met dans sa propre station (0xc40e4-0xc4254) et lit dans celle du pair (0x1604a0).
//
// A defaut (stations du seul Register, natm=0), celui de la station qu'elle a fait sonder
// en dernier (RequestProbeInitiationExt, pushInitiateProbe). Prod du 17/10 00:30Z a
// 03:40Z : 38 verdicts de classe « ? » venaient de consoles qui sondaient leurs pairs sans
// jamais faire de ReplaceURL (Register natm=0, station sondee natm=1), dont 2 echecs 0515
// (01:59:36Z et 01:59:52Z) comptes au seuil de deux.
func natmStation(c *Connection) int {
	st := c.Stations()
	for i := len(st) - 1; i >= 0; i-- {
		if st[i] != nil {
			if m := st[i].GetInt("natm"); m != 0 {
				return m
			}
		}
	}
	return int(c.natmSonde.Load())
}

// classePia : le code d erreur que le jeu afficherait si ce percage echoue
// (NexConnectStationJob 0xf0c80-0xf104c, table 0xbd170) : meme adresse publique -> 0583 a
// 0586 ; sinon d apres le natm local et celui du pair : 1/1 -> 0513, 1/2 -> 0515,
// 2/1 -> 0516, 2/2 -> 0514. « ? » si un natm est inconnu.
func classePia(local, pair *Connection) string {
	return classePiaDe(PublicIPOf(local), PublicIPOf(pair), natmStation(local), natmStation(pair))
}

// classePiaDe : classePia a partir des adresses publiques et des natm (pair deja parti,
// connexionPartie).
func classePiaDe(ipLocal, ipPair string, natmLocal, natmPair int) string {
	if ipLocal != "" && ipLocal == ipPair {
		return "0583-0586"
	}
	switch [2]int{natmLocal, natmPair} {
	case [2]int{1, 1}:
		return "0513"
	case [2]int{1, 2}:
		return "0515"
	case [2]int{2, 1}:
		return "0516"
	case [2]int{2, 2}:
		return "0514"
	}
	return "?"
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
