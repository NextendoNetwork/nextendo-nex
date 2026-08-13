package nex

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Matchmaking protocols and the methods MK8 uses.
const (
	ProtocolMatchMaking        uint16 = 0x15
	ProtocolMatchmakeExtension uint16 = 0x6D
	ProtocolMatchMakingExt     uint16 = 0x32

	// MatchMaking (0x15)
	MethodUnregisterGathering uint32 = 0x02
	MethodFindBySingleID15    uint32 = 0x15
	MethodUpdateSessionURL    uint32 = 0x1B
	MethodUpdateSessionHostV1 uint32 = 0x28
	MethodGetSessionURLs      uint32 = 0x29
	MethodUpdateSessionHost   uint32 = 0x2A

	// Splatfest TEAM battles combine two full 4-player teams by MIGRATING gathering ownership
	// (kinnay MatchMaking 0x15: 43 UpdateGatheringOwnership, 44 MigrateGatheringOwnership, and
	// the V1 36 without a participants_only bool) — NOT by a plain join, which is why autoMatchmake
	// alone leaves both teams stuck on "Waiting for players". Splatoon 1's private splatfest
	// re-creation confirmed these are the only extra methods a fest needs over normal matchmaking.
	MethodMigrateGatheringOwnershipV1 uint32 = 0x24 // 36
	MethodUpdateGatheringOwnership    uint32 = 0x2B // 43 -> returns bool
	MethodMigrateGatheringOwnership   uint32 = 0x2C // 44

	// MatchmakeExtension (0x6D)
	// CloseParticipation(gid) locks a gathering against new joiners — the game calls it to
	// start the match (Splatoon 2 fires it once matchmaking gives up filling the lobby).
	// Answering NotImplemented here aborts S2's online session with 2306-0103 "an error has
	// occurred": measured by capturing both servers with the same client, this was the LAST
	// call before the error, and the proven server answers it and carries on.
	MethodCloseParticipation              uint32 = 0x01
	MethodOpenParticipation               uint32 = 0x02
	MethodCreateMatchmakeSessionWithParam uint32 = 0x26
	MethodJoinMatchmakeSessionWithParam   uint32 = 0x27
	MethodAutoMatchmakeWithParamPostpone  uint32 = 0x28

	// UpdateApplicationBuffer(gid, buffer) is how a HOST publishes its match configuration
	// into the gathering — Smash sends ~422 bytes of it as soon as a lobby forms. Joiners
	// read it back off the session, so refusing it leaves them with a session they cannot
	// interpret and the lobby loops back to matchmaking (2618-0006). The nex-go stack
	// implemented it (nex-protocols-common-go/matchmake-extension/update_application_buffer.go)
	// and this core did not — a host's buffer was silently rejected on every lobby.
	MethodUpdateApplicationBuffer uint32 = 0x0B

	// ModifyCurrentGameAttribute(gid, attribIndex, newValue) is what Splatoon 2's HOST calls
	// at the 8-player match start to restamp one attribute of the gathering as the lobby locks
	// (it fires twice). Joiners read the session attributes back, so we apply it in place.
	// The contract the client blocks on is simply an ANSWER: NotImplemented here aborts the
	// start with 2306-0103 "an error has occurred" (and, mid-transition, a client-side stack
	// overflow) — the exact wall Turf War hit the moment lobbies could finally fill to 8.
	MethodModifyCurrentGameAttribute uint32 = 0x08

	// UpdateMatchmakeSessionPart(param) is what Splatoon 2 calls at the END of a Salmon Run
	// co-op session, after the result report, to run its finish transition. It updates a
	// subset of the session it no longer needs us to keep — nothing downstream reads it, so
	// an empty success is the whole contract. Answering NotImplemented instead aborts the
	// finish with 2306-0103 right after the results screen, which is what players saw.
	MethodUpdateMatchmakeSessionPart     uint32 = 0x2C
	MethodUpdateProgressScore            uint32 = 0x22
	MethodFindMatchmakeSessionBySingleID uint32 = 0x31
	MethodCustomPlayingSession           uint32 = 0x3C
	MethodCustomFriendsQuery             uint32 = 0x41
	MethodCustomPrivateRoomCreate        uint32 = 0x44
	MethodCustomResolveCode              uint32 = 0x45

	// SSBU (Super Smash Bros Ultimate) arena methods. MK8 never calls these, so
	// registering them is harmless to MK8; they reuse the same in-memory store.
	MethodFindByParticipant uint32 = 0x33 // FindMatchmakeSessionByParticipant → empty at startup
	MethodBrowseNoHolder    uint32 = 0x34 // BrowseMatchmakeSessionNoHolderNoResultRange (public arena search)
	MethodSSBUArenaCode     uint32 = 0x37 // arena code create (same struct as MK8 0x44)
	MethodSSBUArenaResolve  uint32 = 0x38 // resolve arena code → bare u32 gid (like 0x45)
	MethodFindByGidList     uint32 = 0x30 // FindMatchmakeSessionByGatheringID (list<gid> → list<session>)
	MethodSSBUPreMatch      uint32 = 0x3A // pre-AutoMatchmake probe → bare u32 = 2 (captured Nintendo value)
	// Tournois Mario Kart 8, relevés sur la capture du serveur de Nintendo :
	// 0x39 recherche (critère → liste), 0x3F inscription (id → réponse vide).
	// 0x3A est partagé avec la sonde SSBU ci-dessus, d'où le tri par forme.
	MethodTournamentSearch uint32 = 0x39
	MethodTournamentJoin   uint32 = 0x3F

	// MatchMakingExt (0x32)
	MethodEndParticipation uint32 = 0x1
)

type gathering struct {
	session      *MatchmakeSession
	participants []uint64 // participant PIDs (index 0 = owner/host)
	hostConnID   uint32   // the host's connection id (for station URLs + notifications)
	code         string   // private-room code (0x44)
}

// Matchmaking is the in-memory matchmaking store + handlers, replicating the
// proven server's session lifecycle (create/join, participant tracking) and the
// server→client Participate notification the console's Pia waits on.
type Matchmaking struct {
	// FindByParticipantEnabled fait répondre RÉELLEMENT à FindMatchmakeSessionByParticipant
	// (0x6D.0x33) au lieu d'une liste vide. Animal Crossing s'en sert pour la visite d'île
	// entre amis ; Smash n'appelle cette méthode qu'au démarrage et attend une liste vide,
	// donc le défaut (false) préserve son comportement actuel.
	FindByParticipantEnabled bool
	// SessionPartPersists fait ÉCRIRE les mises à jour partielles de session (0x6D.0x2C)
	// au lieu de simplement les acquitter. Animal Crossing y envoie le « Dodo Code » de
	// l'hôte ; Splatoon 2 n'appelle cette méthode qu'en fin de partie coop, où seul
	// l'acquittement compte — d'où le défaut à false, qui laisse S2 inchangé.
	SessionPartPersists bool
	// PublicStationFirst met la station PUBLIQUE en TÊTE de la liste rendue par
	// GetSessionURLs (et pose Pa=<adresse publique>), au lieu de [lan, public]/Pa=<privée>.
	// La Pia d'Animal Crossing prend la 1ʳᵉ station comme candidat P2P primaire et lit le Pa
	// du candidat public comme cible : sans ça le visiteur sonde l'IP LAN de l'hôte et cale
	// sur « Getting ready to depart » (2618-0502). MK8/S2 gardent le défaut (false), forme
	// éprouvée contre leur serveur.
	PublicStationFirst bool
	// JoinRespExistingCount fait renvoyer, dans la réponse à JoinMatchmakeSessionWithParam, le
	// nombre de participants AVANT l'ajout du visiteur (comme nex-go) au lieu d'après. La Pia
	// d'Animal Crossing dimensionne son maillage P2P sur ce nombre : compté inclus, le visiteur
	// attend une « autre console » fantôme et échoue (2618-0502). Défaut false : MK8 (AutoMatchmake)
	// et SSBU gardent le comptage après-ajout avec lequel ils fonctionnent.
	JoinRespExistingCount bool
	// FriendPIDs fournit la liste d'amis d'un joueur, pour les données de notification
	// entre amis (méthodes 9/10/13). nil = aucun ami, donc listes vides : le comportement
	// des jeux qui n'utilisent pas ce mécanisme.
	FriendPIDs func(pid uint64) []uint64
	// FriendName gives a friend's display name for the entries of method 15. nil = "".
	FriendName func(pid uint64) string
	// OnFriendSessionCreated, when set, is called right after a host creates a session
	// (pid, gid). Titles whose client never publishes its own "room open" notification
	// data use it to publish on the host's behalf. nil by default, so the titles that
	// publish themselves are unchanged.
	OnFriendSessionCreated func(pid uint64, gid uint32)

	notif      *notifStore
	mu         sync.Mutex
	gatherings map[uint32]*gathering
	byCode     map[string]uint32
	nextGID    uint32
	codeRand   uint32
}

// NewMatchmaking returns an empty store.
func NewMatchmaking() *Matchmaking {
	return &Matchmaking{gatherings: map[uint32]*gathering{}, byCode: map[string]uint32{}, nextGID: 1, codeRand: 0x1234, notif: newNotifStore()}
}

// ExtensionHandler handles MatchmakeExtension (0x6D).
func (m *Matchmaking) ExtensionHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodCloseParticipation, MethodOpenParticipation:
			return m.setParticipation(conn, req, req.Method == MethodOpenParticipation)
		case MethodAutoMatchmakeWithParamPostpone:
			return m.autoMatchmake(conn, req)
		case MethodCreateMatchmakeSessionWithParam:
			return m.createSession(conn, req)
		case MethodJoinMatchmakeSessionWithParam:
			return m.joinSession(conn, req)
		case MethodFindMatchmakeSessionBySingleID:
			return m.findBySingleID(conn, req)
		case MethodCustomPrivateRoomCreate:
			return m.privateRoomCreate(conn, req)
		case MethodCustomResolveCode:
			return m.resolveCode(conn, req)
		case MethodCustomPlayingSession:
			return m.playingSession(conn, req)
		case MethodFindByParticipant:
			// Réponse réelle uniquement là où le jeu s'en sert (Animal Crossing) ; ailleurs
			// on conserve la liste vide sur laquelle Smash s'appuie au démarrage.
			if m.FindByParticipantEnabled {
				return m.findByParticipant(conn, req)
			}
			return emptyList(conn, ProtocolMatchmakeExtension, req)
		case MethodCustomFriendsQuery:
			return emptyList(conn, ProtocolMatchmakeExtension, req)
		case MethodUpdateNotificationData:
			return m.updateNotificationData(conn, req)
		case MethodGetFriendNotificationData:
			return m.getFriendNotificationData(conn, req, false)
		case MethodGetFriendNotificationDataList:
			return m.getFriendNotificationData(conn, req, true)
		case MethodUpdateMatchmakeSessionPart:
			return m.updateSessionPart(conn, req)
		case MethodUpdateApplicationBuffer:
			return m.updateApplicationBuffer(conn, req)
		case MethodModifyCurrentGameAttribute:
			return m.modifyCurrentGameAttribute(conn, req)
		case MethodSSBUArenaCode:
			// SSBU arena code create — identical { String code, DateTime } struct as MK8 0x44.
			return m.privateRoomCreate(conn, req)
		case MethodSSBUArenaResolve:
			// SSBU resolve arena code → bare u32 gid (same shape as 0x45).
			return m.resolveCode(conn, req)
		case MethodFindByGidList:
			return m.findByGidList(conn, req)
		case MethodBrowseNoHolder:
			return m.browseNoHolder(conn, req)
		case MethodSSBUPreMatch:
			// MÊME NUMÉRO, DEUX JEUX. Pour SSBU c'est la sonde qui précède
			// AutoMatchmake, et la console réelle recevait u32=2. Pour Mario Kart
			// c'est « donne-moi ces tournois » : le jeu envoie une liste d'ids et
			// attend une LISTE en retour. Lui répondre 2 lui faisait lire deux
			// entrées inexistantes au-delà du tampon.
			//
			// On distingue par la forme de la requête, mesurée sur capture : MK8
			// envoie u32 nombre + nombre × u32, donc exactement 4+4n octets.
			if isTournamentIDList(req.Body) {
				return m.tournamentsByIDs(conn, req)
			}
			out := NewStreamOut(conn.Settings)
			out.U32(2)
			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
		case MethodTournamentSearch:
			return m.searchTournaments(conn, req)
		case MethodTournamentCreate:
			return m.createTournament(conn, req)
		case MethodTournamentDelete:
			return m.removeTournament(conn, req)
		case MethodTournamentJoin:
			return m.registerTournament(conn, req)
		case MethodUpdateProgressScore:
			// The console updates its progress score during the session-keep loop.
			// The proven server just acks (no body); refusing it stalls the loop.
			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
		default:
			return notImplemented(conn, ProtocolMatchmakeExtension, req)
		}
	}
}

// MatchMakingHandler handles MatchMaking (0x15).
func (m *Matchmaking) MatchMakingHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodGetSessionURLs:
			return m.getSessionURLs(conn, req)
		case MethodUnregisterGathering:
			m.unregister(conn, req)
			return boolResponse(conn, ProtocolMatchMaking, req, true)
		case MethodUpdateSessionURL:
			return boolResponse(conn, ProtocolMatchMaking, req, true)
		case MethodUpdateSessionHost, MethodUpdateSessionHostV1:
			return NewRMCSuccess(conn.Settings, ProtocolMatchMaking, req.Method, req.CallID, nil)
		case MethodMigrateGatheringOwnership:
			return m.migrateGatheringOwnership(conn, req, true)
		case MethodMigrateGatheringOwnershipV1:
			return m.migrateGatheringOwnership(conn, req, false)
		case MethodUpdateGatheringOwnership:
			return m.updateGatheringOwnership(conn, req)
		case MethodFindBySingleID15:
			return m.findBySingleID15(conn, req)
		default:
			return notImplemented(conn, ProtocolMatchMaking, req)
		}
	}
}

// MatchMakingExtHandler handles MatchMakingExt (0x32).
func (m *Matchmaking) MatchMakingExtHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodEndParticipation:
			m.endParticipation(conn, req)
			return boolResponse(conn, ProtocolMatchMakingExt, req, true)
		default:
			return notImplemented(conn, ProtocolMatchMakingExt, req)
		}
	}
}

func boolResponse(conn *Connection, proto uint16, req *RMCMessage, v bool) *RMCMessage {
	out := NewStreamOut(conn.Settings)
	out.Bool(v)
	return NewRMCSuccess(conn.Settings, proto, req.Method, req.CallID, out.Bytes())
}

func emptyList(conn *Connection, proto uint16, req *RMCMessage) *RMCMessage {
	out := NewStreamOut(conn.Settings)
	out.U32(0)
	return NewRMCSuccess(conn.Settings, proto, req.Method, req.CallID, out.Bytes())
}

// SimplePlayingSession is one element of the 0x6D/0x3C response: it tells the
// console which gathering a given friend PID is currently in. Wire layout (with
// struct header): PID(8) + GatheringID(u32) + GameMode(u32) + Attribute0(u32).
type SimplePlayingSession struct {
	PrincipalID uint64
	GatheringID uint32
	GameMode    uint32
	Attribute0  uint32
}

// Levels implements Structure.
func (s *SimplePlayingSession) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.PID(s.PrincipalID)
			o.U32(s.GatheringID)
			o.U32(s.GameMode)
			o.U32(s.Attribute0)
		},
		Load: func(i *StreamIn) {
			s.PrincipalID = i.PID()
			s.GatheringID = i.U32()
			s.GameMode = i.U32()
			s.Attribute0 = i.U32()
		},
	}}
}

// playingSession answers CustomGetSimplePlayingSession (0x6D/0x3C). The console's
// friend list calls this with the PIDs of friends it wants to join; for each PID
// that is actually in a live gathering we return that gathering's id, so the
// joiner can then FindMatchmakeSessionBySingleID + JoinMatchmakeSession. Returning
// an empty list (what mk8cs did before) makes every friend-join fail because the
// joiner never learns which session to join. Request = list<PID> + bool
// (include the caller's own session).
func (m *Matchmaking) playingSession(conn *Connection, req *RMCMessage) *RMCMessage {
	in := NewStreamIn(req.Body, conn.Settings)
	pids := ReadList(in, func(i *StreamIn) uint64 { return i.PID() })
	includeSelf := in.Bool()

	m.mu.Lock()
	var sessions []*SimplePlayingSession
	for _, pid := range pids {
		if pid == conn.PID && !includeSelf {
			continue
		}
		for gid, g := range m.gatherings {
			if !containsPID(g.participants, pid) {
				continue
			}
			var attr0 uint32
			if len(g.session.Attribs) > 0 {
				attr0 = g.session.Attribs[0]
			}
			sessions = append(sessions, &SimplePlayingSession{
				PrincipalID: pid,
				GatheringID: gid,
				GameMode:    g.session.GameMode,
				Attribute0:  attr0,
			})
			break
		}
	}
	m.mu.Unlock()

	out := NewStreamOut(conn.Settings)
	WriteList(out, sessions, func(o *StreamOut, s *SimplePlayingSession) { o.Add(s) })
	fmt.Printf("[MM] CustomGetSimplePlayingSession caller=%d asked=%d -> %d live session(s)\n", conn.PID, len(pids), len(sessions))
	return NewRMCSuccess(conn.Settings, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

// finalizeCreatedSession sets every field the server owns on a freshly created
// session; a session missing these is rejected by the console.
// openParticipationDefault reports whether a freshly created session must be marked
// open + active so other players can be matched into it. Off unless the game server sets
// NEXTENDO_OPEN_PARTICIPATION=1.
func openParticipationDefault() bool {
	v := os.Getenv("NEXTENDO_OPEN_PARTICIPATION")
	return v == "1" || v == "true"
}

func finalizeCreatedSession(session *MatchmakeSession, gid uint32, ownerPID uint64) {
	session.ID = gid
	session.OwnerPID = ownerPID
	session.HostPID = ownerPID
	if session.MaxParticipants == 0 {
		session.MaxParticipants = 12
	}
	session.NumParticipants = 1
	session.SessionKey = randomBytes(32)
	session.StartedTime = NowDateTime()
	session.SystemPasswordEnabled = false
	// Optional (NEXTENDO_OPEN_PARTICIPATION=1), OFF by default.
	//
	// autoMatchmake only ever joins a candidate whose OpenParticipation is set. Titles
	// whose client fills that flag itself are matched normally; for a title whose client
	// leaves it clear, EVERY lobby looks closed, so each player opens their own and waits
	// alone forever (the game then times out with a communication error). Marking the
	// session open + active at creation makes those hosts findable. Enabled per game
	// server so the titles that already match correctly are untouched.
	if openParticipationDefault() {
		session.UserPasswordEnabled = false
		session.State = 1 // open / active
		session.OpenParticipation = true
	}
	for len(session.Attribs) < 6 {
		session.Attribs = append(session.Attribs, 0)
	}
	if session.Param.Params == nil {
		session.Param.Params = map[string]Variant{}
	}
	session.Param.Params["@SR"] = Variant{Type: VariantBool, Bool: true}
	session.Param.Params["@GIR"] = Variant{Type: VariantInt64, Int: 3}
}

// createGathering registers a new gathering owned by the caller (mutex held).
func (m *Matchmaking) createGathering(conn *Connection, src *MatchmakeSession) *gathering {
	gid := m.nextGID
	m.nextGID++
	session := *src
	finalizeCreatedSession(&session, gid, conn.PID)
	g := &gathering{session: &session, participants: []uint64{conn.PID}, hostConnID: conn.ID}
	m.gatherings[gid] = g
	return g
}

// notifyJoin pushes the Participate (3001) notification to the gathering owner
// when a new player (not the owner) joins — the event Pia's WaitNotification
// blocks on. The joiner is never notified of its own join.
// notifyParticipation replicates the proven server's join notifications, taken
// byte-for-byte from a live 2-player capture:
//  1. Participate(3001) about the CALLER (Param2=caller) -> pushed to EVERY current
//     participant, INCLUDING the caller itself. The deployed server does NOT self-
//     exclude: a lone host needs its own event to leave Pia's WaitNotification wall
//     (the solo fix), and existing peers must learn of the joiner.
//  2. Recap: for each PRE-EXISTING participant p, Participate(3001) with Param2=p and
//     PIDSource=caller -> pushed to the CALLER, so a joiner learns who is already in
//     the lobby. Without this recap Pia never builds the multi-node mesh and a
//     friend-join dies with "A communication error has occurred".
//
// Sent synchronously so the pushes precede the RMC response on the wire (proven order).
func (m *Matchmaking) notifyParticipation(caller *Connection, participants []uint64, gid uint32, joinMessage string) {
	count := len(participants)
	ep := caller.Endpoint
	// 1. Announce the caller to every participant (self included).
	for _, pid := range participants {
		target := ep.FindConnectionByPID(pid)
		if target == nil {
			continue
		}
		SendNotification(target, &NotificationEvent{
			PIDSource: caller.PID, Type: NotificationParticipate,
			Param1: uint64(gid), Param2: caller.PID, StrParam: joinMessage, Param3: uint64(count),
		})
	}
	// 2. Recap to the caller: each pre-existing participant.
	for _, pid := range participants {
		if pid == caller.PID {
			continue
		}
		SendNotification(caller, &NotificationEvent{
			PIDSource: caller.PID, Type: NotificationParticipate,
			Param1: uint64(gid), Param2: pid, StrParam: joinMessage, Param3: uint64(count),
		})
	}
	fmt.Printf("[MM] participation gid=%d caller=%d -> announce to %d participant(s) + recap %d existing (participants=%v)\n",
		gid, caller.PID, count, count-1, participants)
}

// [Nextendo] NAT-aware matchmaking. A symmetric-mapping ("Strict") NAT cannot hole-punch to
// another Strict peer, nor reliably to a Moderate one — so dropping such a pair into the same
// lobby gives EVERYONE in it "A communication error has occurred" (2618-0510). We classify each
// peer from its ReportNATProperties (natMap/natFilter — same buckets as the monitoring site) and
// refuse to place a joiner into a lobby it cannot reach; it then looks for another lobby, and if
// none is compatible it opens its own (so a bad-NAT player no longer poisons everyone else).
type natClass int

const (
	natUnknown natClass = iota // not reported yet -> stay optimistic (fail-open)
	natOpen
	natModerate
	natStrict
)

func (c natClass) String() string {
	switch c {
	case natOpen:
		return "open"
	case natModerate:
		return "moderate"
	case natStrict:
		return "strict"
	default:
		return "unknown"
	}
}

// classifyNAT buckets a client's reported NAT behaviour. Mirrors the dashboard's natTypeLabel:
// (0,0)=unknown, map&filter<=1=open, map<=1=moderate, else strict (symmetric mapping).
func classifyNAT(natMap, natFilter uint32) natClass {
	if natMap == 0 && natFilter == 0 {
		return natUnknown
	}
	if natMap <= 1 && natFilter <= 1 {
		return natOpen
	}
	if natMap <= 1 {
		return natModerate
	}
	return natStrict
}

// natCompatible reports whether two peers can likely hole-punch each other. Unknown is
// optimistic (fail-open, so we never wrongly isolate a player we can't yet classify). Only the
// pairs that reliably fail are rejected: strict×strict and strict×moderate.
func natCompatible(a, b natClass) bool {
	if a == natUnknown || b == natUnknown {
		return true
	}
	if a == natStrict && (b == natStrict || b == natModerate) {
		return false
	}
	if b == natStrict && (a == natStrict || a == natModerate) {
		return false
	}
	return true
}

// pidNATClass reads a participant's NAT class from its live connection (unknown if it's gone).
func pidNATClass(ep *Endpoint, pid uint64) natClass {
	if ep == nil {
		return natUnknown
	}
	c := ep.FindConnectionByPID(pid)
	if c == nil {
		return natUnknown
	}
	return classifyNAT(c.NATBehaviour())
}

// gatheringNATCompatible reports whether a joiner of the given class can hole-punch with EVERY
// current member of the gathering.
func gatheringNATCompatible(ep *Endpoint, joiner natClass, members []uint64, joinerPID uint64) bool {
	for _, pid := range members {
		if pid == joinerPID {
			continue
		}
		if !natCompatible(joiner, pidNATClass(ep, pid)) {
			return false
		}
	}
	return true
}

func (m *Matchmaking) autoMatchmake(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	var param AutoMatchmakeParam
	in := NewStreamIn(req.Body, s)
	in.Extract(&param)
	if in.Err() != nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}
	src := &param.Session

	// [Nextendo] NAT-aware: the joiner's own class, and the endpoint used to look up members.
	ep := conn.Endpoint
	joinerClass := classifyNAT(conn.NATBehaviour())

	m.mu.Lock()
	var g *gathering
	skippedNAT := 0
	// [Nextendo] A solo-isolated PID (NEX_SOLO_PIDS) never joins an existing lobby: it always
	// opens its own gathering. Gives a private test account a reliable solo online lobby on a
	// SHARED server, sidestepping the P2P/NAT hole-punch that fails (2618-0521) when it gets
	// matched with real, unreachable consoles.
	soloForced := isSoloPID(conn.PID)
	if !soloForced {
		for _, cand := range m.gatherings {
			if cand.session.GameMode == src.GameMode &&
				cand.session.OpenParticipation &&
				!cand.session.UserPasswordEnabled &&
				uint16(len(cand.participants)) < cand.session.MaxParticipants {
				// Skip a lobby the joiner can't hole-punch with (would give everyone 2618-0510);
				// keep looking for a compatible one.
				if !gatheringNATCompatible(ep, joinerClass, cand.participants, conn.PID) {
					skippedNAT++
					continue
				}
				g = cand
				break
			}
		}
	}
	joined := false
	if g == nil {
		g = m.createGathering(conn, src)
		if soloForced {
			// Le client demande minP=2 → une partie solo (count=1) ne démarre jamais et re-matchmake
			// en boucle. On force minP=1 pour ce PID isolé : count=1 suffit → la course démarre solo.
			g.session.MinParticipants = 1
			fmt.Printf("[MM] solo-isolate: pid=%d -> own gid=%d minP=1 (skip join, test account)\n", conn.PID, g.session.ID)
		} else if skippedNAT > 0 {
			fmt.Printf("[MM] NAT-aware: pid=%d class=%s incompatible with %d open lobby(ies) -> opened own gid=%d\n",
				conn.PID, joinerClass, skippedNAT, g.session.ID)
		}
	} else if !containsPID(g.participants, conn.PID) {
		g.participants = append(g.participants, conn.PID)
		joined = true
	}
	g.session.NumParticipants = uint32(len(g.participants))
	result := *g.session
	gid, count := g.session.ID, len(g.participants)
	parts := append([]uint64(nil), g.participants...)
	out := NewStreamOut(s)
	out.Add(&result)
	m.mu.Unlock()

	fmt.Printf("[MM] autoMatchmake pid=%d -> gid=%d owner=%d host=%d joined=%v count=%d mode=%d flags=%d state=%d minP=%d maxP=%d skLen=%d attribs=%v respLen=%d\n  resp=%x\n",
		conn.PID, gid, result.OwnerPID, result.HostPID, joined, count, result.GameMode, result.Flags, result.State, result.MinParticipants, result.MaxParticipants, len(result.SessionKey), result.Attribs, len(out.Bytes()), out.Bytes())
	// Always notify the owner of the participation — including the lone-host self-join
	// (joined==false), which is the case Pia needs to leave the WaitNotification wall.
	_ = joined
	m.notifyParticipation(conn, parts, gid, "")
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) createSession(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	var param AutoMatchmakeParam
	in := NewStreamIn(req.Body, s)
	in.Extract(&param)
	if in.Err() != nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}

	m.mu.Lock()
	g := m.createGathering(conn, &param.Session)
	result := *g.session
	gid := g.session.ID
	parts := append([]uint64(nil), g.participants...)
	out := NewStreamOut(s)
	out.Add(&result)
	m.mu.Unlock()

	fmt.Printf("[MM] createSession pid=%d -> gid=%d owner=%d\n", conn.PID, result.ID, result.OwnerPID)
	if m.OnFriendSessionCreated != nil {
		m.OnFriendSessionCreated(conn.PID, gid)
	}
	// Self-notify the host (same as autoMatchmake) so a friend-room/tournament lobby
	// held solo also leaves Pia's WaitNotification wall instead of 2618-562.
	m.notifyParticipation(conn, parts, gid, "")
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) joinSession(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	// JoinMatchmakeSessionParam: struct header + GID:u32 + ... (we only need the GID).
	in := NewStreamIn(req.Body, s)
	_ = in.U8() // struct version
	sub := in.Substream()
	gid := sub.U32()

	m.mu.Lock()
	g := m.gatherings[gid]
	if g == nil {
		m.mu.Unlock()
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousSessionVoid)
	}
	existing := len(g.participants) // participants DÉJÀ présents, AVANT d'ajouter l'appelant
	joined := false
	if !containsPID(g.participants, conn.PID) {
		// Refuser d'entrer dans un salon PLEIN. Sans ce garde-fou, le Join direct (friend-join,
		// join par code) empile un joueur au-delà du maximum de la session — relevé en prod : un
		// salon en 9/8. On respecte le MaxParticipants PROPRE à la session (fixé par le jeu) :
		// 12 pour MK8, 8 pour Splatoon 2 (10 en privé), la taille configurée pour Smash/Salmon Run.
		// Le client rebascule alors sur un autre salon au lieu de casser un match plein.
		if g.session.MaxParticipants > 0 && uint16(len(g.participants)) >= g.session.MaxParticipants {
			m.mu.Unlock()
			return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousSessionFull)
		}
		g.participants = append(g.participants, conn.PID)
		joined = true
	}
	g.session.NumParticipants = uint32(len(g.participants))
	result := *g.session
	// La réponse au JOIN doit annoncer le nombre de participants tel que le serveur nex-go de
	// référence le renvoie : le compte AVANT ajout du visiteur (juste l'hôte = 1), PAS après (2).
	// La Pia d'Animal Crossing bâtit son maillage P2P d'après ce nombre : compté inclus (2), le
	// visiteur attend une 2ᵉ « autre console » qui n'existe pas et échoue sur « one or more other
	// consoles are not responding » (2618-0502). Mesuré : nex-go Join/39 = NumParticipants 1.
	if m.JoinRespExistingCount && joined {
		result.NumParticipants = uint32(existing)
	}
	parts := append([]uint64(nil), g.participants...)
	out := NewStreamOut(s)
	out.Add(&result)
	m.mu.Unlock()

	if joined {
		m.notifyParticipation(conn, parts, gid, "")
	}
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) findBySingleID(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	gid := NewStreamIn(req.Body, s).U32()

	m.mu.Lock()
	g := m.gatherings[gid]
	var result *MatchmakeSession
	if g != nil {
		r := *g.session
		r.SessionKey = nil // read-by-id scrubs the key + password
		r.UserPassword = ""
		result = &r
	}
	m.mu.Unlock()

	if result == nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousSessionVoid)
	}
	out := NewStreamOut(s)
	out.Add(result)
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

// findByGidList answers SSBU's FindMatchmakeSessionByGatheringID (0x6D/0x30):
// given a list<u32> of gathering ids, return u32 count + each MatchmakeSession
// body. SSBU calls this right after resolving an arena code (0x38) to fetch the
// session before JoinMatchmakeSessionWithParam (0x27).
func (m *Matchmaking) findByGidList(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	gids := ReadList(in, func(i *StreamIn) uint32 { return i.U32() })

	m.mu.Lock()
	var sessions []*MatchmakeSession
	for _, gid := range gids {
		if g := m.gatherings[gid]; g != nil {
			r := *g.session
			r.SessionKey = nil
			r.UserPassword = ""
			sessions = append(sessions, &r)
		}
	}
	m.mu.Unlock()

	out := NewStreamOut(s)
	out.U32(uint32(len(sessions)))
	for _, ss := range sessions {
		out.Add(ss)
	}
	fmt.Printf("[MM] findByGidList gids=%v -> %d session(s)\n", gids, len(sessions))
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

// browseNoHolder answers SSBU's BrowseMatchmakeSessionNoHolderNoResultRange
// (0x6D/0x34): the public-arena search. The request is a search criteria we don't
// strictly need to parse — return every OPEN session as a raw list<MatchmakeSession>
// (u32 count + each body, NO DataHolder wrapper), scrubbing the key + password.
// Fail-soft: an empty list shows "no arenas" instead of a comm error.
func (m *Matchmaking) browseNoHolder(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings

	// Les critères portent le « Dodo Code » d'Animal Crossing dans leur champ CodeWord
	// (NEX 4.0.0). On les lit de façon défensive : un corps illisible ou un code vide
	// laisse la recherche publique d'origine intacte, celle sur laquelle Smash s'appuie.
	var criteria MatchmakeSessionSearchCriteria
	if len(req.Body) > 0 {
		in := NewStreamIn(req.Body, s)
		in.Extract(&criteria)
		if in.Err() != nil {
			criteria.Codeword = ""
		}
	}
	wantCode := criteria.Codeword

	m.mu.Lock()
	var sessions []*MatchmakeSession
	for _, g := range m.gatherings {
		if !g.session.OpenParticipation {
			continue
		}
		if wantCode != "" {
			// Recherche par code : le code EST le laissez-passer, on ne réapplique donc
			// pas le filtre « sans mot de passe » qui exclurait justement ces sessions.
			if g.session.Codeword != wantCode && g.code != wantCode {
				continue
			}
		} else if g.session.UserPasswordEnabled {
			continue
		}
		r := *g.session
		// Recherche par Dodo Code (ACNH, wantCode!="") : le demandeur détient le code, il est
		// donc autorisé à rejoindre → on GARDE la clé de session, comme nex-go et comme
		// findByParticipant, sinon la visite cale (2618-0502). Recherche publique sans code
		// (arène Smash) : on la retire pour ne pas la divulguer à un simple scan.
		if wantCode == "" {
			r.SessionKey = nil
		}
		r.UserPassword = ""
		sessions = append(sessions, &r)
	}
	m.mu.Unlock()

	if wantCode != "" {
		fmt.Printf("[MM] browseNoHolder code=%q -> %d session(s)\n", wantCode, len(sessions))
		out := NewStreamOut(s)
		out.U32(uint32(len(sessions)))
		for _, ss := range sessions {
			out.Add(ss)
		}
		return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
	}

	out := NewStreamOut(s)
	out.U32(uint32(len(sessions)))
	for _, ss := range sessions {
		out.Add(ss)
	}
	fmt.Printf("[MM] browseNoHolder -> %d public session(s)\n", len(sessions))
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

// findBySingleID15 is MatchMaking 0x15/0x15 → (found:bool, gathering).
func (m *Matchmaking) findBySingleID15(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	gid := NewStreamIn(req.Body, s).U32()
	m.mu.Lock()
	g := m.gatherings[gid]
	out := NewStreamOut(s)
	if g == nil {
		out.Bool(false)
	} else {
		out.Bool(true)
		r := *g.session
		out.Add(&r)
	}
	m.mu.Unlock()
	return NewRMCSuccess(s, ProtocolMatchMaking, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) privateRoomCreate(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	gid := NewStreamIn(req.Body, s).U32()
	m.mu.Lock()
	code := m.makeCode(gid)
	m.mu.Unlock()

	// Response: struct header wrapping [String code + u64 datetime(0)].
	body := NewStreamOut(s)
	body.String(code)
	body.U64(0)
	out := NewStreamOut(s)
	out.U8(0) // struct version
	out.Buffer(body.Bytes())
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) resolveCode(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	code := NewStreamIn(req.Body, s).String()
	if len(code) != 5 && len(req.Body) > 5 {
		code = NewStreamIn(req.Body[5:], s).String()
	}
	m.mu.Lock()
	gid := m.byCode[code]
	m.mu.Unlock()
	out := NewStreamOut(s)
	out.U32(gid)
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) getSessionURLs(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	gid := NewStreamIn(req.Body, s).U32()
	m.mu.Lock()
	g := m.gatherings[gid]
	m.mu.Unlock()

	var host *Connection
	if g != nil {
		host = conn.Endpoint.FindConnectionByID(g.hostConnID)
	}

	if host == nil {
		return sessionURLsResponse(conn, req, nil)
	}

	// Hand over the host's REAL UDP endpoint, not the WebSocket TCP port it registered:
	// the latter is unreachable for Pia's hole-punch, so the joiner's probe never lands
	// and the console stalls at MatchMakingExt m=1.
	urls, status := natBridgeStations(host.Stations(), m.PublicStationFirst)
	if status != bridgeNoRVCID {
		// Either it worked, or nothing a moment brings will change it.
		return sessionURLsResponse(conn, req, urls)
	}

	// The bridge needs the host's ReplaceURL, which carries its post-NAT port and RVCID —
	// and the joiner routinely asks for the host's urls about a second BEFORE the host
	// sends it. Answering now would hand over a station the joiner cannot probe.
	//
	// So wait for it — but NOT here. This runs on the joiner's receive goroutine, and
	// blocking it stops the joiner from acking: its PRUDP window fills, everything
	// retransmits, and the cure is worse than the bug. Hand the wait to its own goroutine
	// and answer from there; SendRMC is safe from any goroutine, and a handler returning
	// nil sends nothing.
	go m.answerSessionURLsWhenHostIsReady(conn, req, host)

	return nil
}

// hostReplaceURLWait bounds how long a joiner waits for the host's ReplaceURL. Long enough
// to cover the ~1s the console takes to report it, short enough that a host which never
// will (already gone, or NAT-checking failed) does not hold the joiner past its own
// timeout — it gets the raw urls and fails the way it would have anyway.
const hostReplaceURLWait = 2500 * time.Millisecond

// hostReplaceURLPoll is how often the wait re-reads the host's stations. The host's own
// goroutine publishes them, so there is nothing to subscribe to; polling ten times a
// second is cheap and bounds the added latency to ~100 ms.
const hostReplaceURLPoll = 100 * time.Millisecond

func (m *Matchmaking) answerSessionURLsWhenHostIsReady(conn *Connection, req *RMCMessage, host *Connection) {
	deadline := time.Now().Add(hostReplaceURLWait)

	for time.Now().Before(deadline) {
		time.Sleep(hostReplaceURLPoll)

		if urls, status := natBridgeStations(host.Stations(), m.PublicStationFirst); status == bridgeOK {
			fmt.Printf("[MM] GetSessionURLs pid=%d: host reported its ReplaceURL -> bridged\n", conn.PID)
			conn.SendRMC(sessionURLsResponse(conn, req, urls))

			return
		}
	}

	// Out of time: answer with whatever the host reported. Not answering at all would
	// leave the joiner hanging until ITS timeout, which is strictly worse.
	fmt.Printf("[MM] GetSessionURLs pid=%d: host never reported its ReplaceURL -> raw stations\n", conn.PID)
	conn.SendRMC(sessionURLsResponse(conn, req, host.Stations()))
}

func sessionURLsResponse(conn *Connection, req *RMCMessage, urls []*StationURL) *RMCMessage {
	s := conn.Settings
	out := NewStreamOut(s)
	WriteList(out, urls, func(o *StreamOut, u *StationURL) { o.StationURL(u) })

	return NewRMCSuccess(s, ProtocolMatchMaking, req.Method, req.CallID, out.Bytes())
}

func (m *Matchmaking) unregister(conn *Connection, req *RMCMessage) {
	gid := NewStreamIn(req.Body, conn.Settings).U32()
	m.mu.Lock()
	g, existed := m.gatherings[gid]
	var owner uint64
	mine := false
	if existed {
		owner = g.session.OwnerPID
		mine = owner == conn.PID
	}
	// [MC churn diag] Only the OWNER may unregister their gathering. A caller unregistering
	// a gathering it does not own would silently delete another player's live lobby (making
	// it un-browsable) — never legitimate. Log every call to see whether MC is tearing down
	// its OWN just-created host session (the suspected churn cause).
	if existed && mine {
		delete(m.gatherings, gid)
	}
	m.mu.Unlock()
	fmt.Printf("[MM] unregister caller=%d gid=%d existed=%v owner=%d mine=%v -> %s\n",
		conn.PID, gid, existed, owner, mine, map[bool]string{true: "REMOVED", false: "kept (not owner / gone)"}[existed && mine])
}

// setParticipation implements MatchmakeExtension CloseParticipation(0x01) /
// OpenParticipation(0x02): the gathering's owner locks or unlocks it against new joiners.
// The request is a bare u32 gathering id; the response is an empty success.
//
// Closing is what a title does when it stops accepting joiners and starts the match, so
// this is on the normal path, not an edge case — Splatoon 2 calls it as soon as
// matchmaking gives up filling the lobby. Answering NotImplemented ends the online session
// with 2306-0103.
func (m *Matchmaking) setParticipation(conn *Connection, req *RMCMessage, open bool) *RMCMessage {
	s := conn.Settings
	gid := NewStreamIn(req.Body, s).U32()

	m.mu.Lock()
	g := m.gatherings[gid]
	if g != nil && g.session != nil {
		g.session.OpenParticipation = open
	}
	m.mu.Unlock()

	what := "Close"
	if open {
		what = "Open"
	}
	if g == nil {
		// Unknown gathering: still answer success. The client is telling us about a lobby
		// it believes in; failing here aborts its session, and there is nothing to protect.
		fmt.Printf("[MM] %sParticipation pid=%d gid=%d (unknown gathering) -> success\n", what, conn.PID, gid)
	} else {
		fmt.Printf("[MM] %sParticipation pid=%d gid=%d\n", what, conn.PID, gid)
	}
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// updateSessionPart implements MatchmakeExtension UpdateMatchmakeSessionPart (0x2C): the
// end-of-session transition Splatoon 2 runs after a Salmon Run co-op result.
//
// The request carries an UpdateMatchmakeSessionParam describing the fields the client
// wants changed. Nothing here reads them: the session is finishing, and every consumer of
// a gathering (join, browse, the dashboard) is done with it by this point. So the reply is
// an empty success — an acknowledgement, which is all the client is waiting for.
//
// What matters is that it is answered at all. NotImplemented aborts the finish with
// 2306-0103 immediately after the results screen. This was implemented in the nex-go stack
// and lost when Splatoon 2 moved to this core.
func (m *Matchmaking) updateSessionPart(conn *Connection, req *RMCMessage) *RMCMessage {
	// Animal Crossing transmet ICI le « Dodo Code » de l hote : sans ecriture reelle, la
	// recherche par code ne peut jamais correspondre. Splatoon 2 n appelle cette methode
	// qu en fin de partie coop, ou seul l acquittement compte — d ou l interrupteur.
	if m.SessionPartPersists {
		var p UpdateMatchmakeSessionParam
		in := NewStreamIn(req.Body, conn.Settings)
		in.Extract(&p)
		if in.Err() == nil {
			if m.applySessionPart(&p) {
				fmt.Printf("[MM] session gid=%d mise a jour (code=%q open=%t) par pid=%d\n", p.GID, p.Codeword, p.OpenParticipation, conn.PID)
			} else {
				fmt.Printf("[MM] UpdateMatchmakeSessionPart : gid=%d inconnu (pid=%d)\n", p.GID, conn.PID)
			}
		} else {
			fmt.Printf("[MM] UpdateMatchmakeSessionPart illisible pid=%d -> simple ack\n", conn.PID)
		}
		return NewRMCSuccess(conn.Settings, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
	}

	fmt.Printf("[MM] UpdateMatchmakeSessionPart pid=%d -> ack (coop finish)\n", conn.PID)

	return NewRMCSuccess(conn.Settings, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// updateApplicationBuffer implements MatchmakeExtension UpdateApplicationBuffer (0x0B):
// the host publishes its match configuration into the gathering.
//
// Request = (u32 gid, Buffer applicationBuffer); the response is an empty success. The
// buffer is opaque to us — it is the title's own payload, and joiners read it straight back
// off the session — so storing it verbatim IS the implementation.
//
// Only the owner may write it, matching the proven server: the buffer describes the match
// the host is running, and letting a joiner overwrite it would let anyone rewrite the rules
// of somebody else's lobby.
func (m *Matchmaking) updateApplicationBuffer(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	gid := in.U32()
	buf := in.Buffer()

	m.mu.Lock()
	g := m.gatherings[gid]
	owner := uint64(0)
	if g != nil && len(g.participants) > 0 {
		owner = g.participants[0]
	}
	if g != nil && g.session != nil && owner == conn.PID {
		g.session.ApplicationData = append([]byte(nil), buf...)
	}
	m.mu.Unlock()

	if g == nil || g.session == nil {
		fmt.Printf("[MM] UpdateApplicationBuffer pid=%d gid=%d -> unknown gathering\n", conn.PID, gid)

		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousSessionVoid)
	}

	if owner != conn.PID {
		fmt.Printf("[MM] UpdateApplicationBuffer pid=%d gid=%d -> denied (owner is %d)\n", conn.PID, gid, owner)

		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreAccessDenied)
	}

	fmt.Printf("[MM] UpdateApplicationBuffer pid=%d gid=%d -> stored %d bytes\n", conn.PID, gid, len(buf))

	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// modifyCurrentGameAttribute implements MatchmakeExtension ModifyCurrentGameAttribute (0x08):
// the host restamps one attribute of its gathering (gid, attribIndex, newValue) as the match
// locks. Splatoon 2 fires it at the 8-player start; joiners read the attributes back, so we
// apply it in place (owner-only, like UpdateApplicationBuffer). We always ack success — the
// point is to answer at all; NotImplemented aborts the start with 2306-0103.
func (m *Matchmaking) modifyCurrentGameAttribute(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	gid := in.U32()
	attribIndex := in.U32()
	newValue := in.U32()

	m.mu.Lock()
	if g := m.gatherings[gid]; g != nil && g.session != nil && len(g.participants) > 0 && g.participants[0] == conn.PID {
		for uint32(len(g.session.Attribs)) <= attribIndex {
			g.session.Attribs = append(g.session.Attribs, 0)
		}
		g.session.Attribs[attribIndex] = newValue
	}
	m.mu.Unlock()

	fmt.Printf("[MM] ModifyCurrentGameAttribute pid=%d gid=%d attr[%d]=%d -> ack\n", conn.PID, gid, attribIndex, newValue)

	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// migrateGatheringOwnership implements MatchMaking MigrateGatheringOwnership (0x2C=44) and its V1
// (0x24=36). The owner is handing the gathering off: pick the new owner from the potential_owners
// list (the first that is still a participant) and move it to the front (index 0 = owner in this
// store), then re-announce participation. Splatoon 2 splatfest TEAM battles combine two 4-player
// teams through this hand-off; the reply is an empty success — answering it is what lets the fest
// flow progress instead of stalling on "Waiting for players".
func (m *Matchmaking) migrateGatheringOwnership(conn *Connection, req *RMCMessage, hasParticipantsOnly bool) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	gid := in.U32()
	candidates := ReadList(in, func(i *StreamIn) uint64 { return i.PID() })
	if hasParticipantsOnly {
		_ = in.Bool()
	}

	var parts []uint64
	m.mu.Lock()
	if g := m.gatherings[gid]; g != nil && len(g.participants) > 0 {
		migrated := false
		for _, c := range candidates {
			for idx, p := range g.participants {
				if p == c {
					g.participants[0], g.participants[idx] = g.participants[idx], g.participants[0]
					migrated = true
					break
				}
			}
			if migrated {
				break
			}
		}
		parts = append([]uint64(nil), g.participants...)
	}
	m.mu.Unlock()

	if parts != nil {
		m.notifyParticipation(conn, parts, gid, "")
	}
	fmt.Printf("[MM] MigrateGatheringOwnership pid=%d gid=%d candidates=%v -> ok\n", conn.PID, gid, candidates)

	return NewRMCSuccess(s, ProtocolMatchMaking, req.Method, req.CallID, nil)
}

// updateGatheringOwnership implements MatchMaking UpdateGatheringOwnership (0x2B=43):
// (gid, participants_only) -> bool. We report the ownership as settled (true). Answering at all
// is what the splatfest team-battle flow needs; NotImplemented aborts it.
func (m *Matchmaking) updateGatheringOwnership(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	gid := in.U32()
	_ = in.Bool()

	out := NewStreamOut(s)
	out.Bool(true)
	fmt.Printf("[MM] UpdateGatheringOwnership pid=%d gid=%d -> true\n", conn.PID, gid)

	return NewRMCSuccess(s, ProtocolMatchMaking, req.Method, req.CallID, out.Bytes())
}

// RemovePlayer drops a PID from every gathering it takes part in, deleting any gathering
// left empty or whose owner just left. Wire it to Endpoint.OnDisconnect: a gathering is
// otherwise only removed when the client politely calls UnregisterGathering /
// EndParticipation, so a client that crashes or errors out (S2 bouncing on 2306-0103, a
// native emulator crash) leaks its lobby forever — the monitoring then shows phantom
// lobbies "searching" with a player who left, and matchmaking can hand those dead sessions
// out to real players.
func (m *Matchmaking) RemovePlayer(pid uint64) {
	if pid == 0 {
		return
	}
	// Un joueur parti n'a plus de porte ouverte : sa donnée de notification doit disparaître
	// avec lui, sinon il resterait proposé au menu de visite de ses amis alors qu'il est hors
	// ligne — exactement le genre de trace fantôme que l'éviction des connexions corrige.
	m.notif.forget(pid)

	m.mu.Lock()
	defer m.mu.Unlock()
	for gid, g := range m.gatherings {
		if g == nil || g.session == nil {
			continue
		}
		before := len(g.participants)
		g.participants = removePID(g.participants, pid)
		if len(g.participants) == before {
			continue // not in this gathering
		}
		// The owner leaving kills the lobby: it is theirs, and nothing here migrates a host.
		if len(g.participants) == 0 || g.session.Gathering.OwnerPID == pid {
			delete(m.gatherings, gid)
			if g.code != "" {
				delete(m.byCode, g.code)
			}
			fmt.Printf("[MM] disconnect pid=%d -> gathering %d removed\n", pid, gid)
			continue
		}
		g.session.NumParticipants = uint32(len(g.participants))
		fmt.Printf("[MM] disconnect pid=%d -> left gathering %d (%d left)\n", pid, gid, len(g.participants))
	}
}

func (m *Matchmaking) endParticipation(conn *Connection, req *RMCMessage) {
	gid := NewStreamIn(req.Body, conn.Settings).U32()
	m.mu.Lock()
	if g := m.gatherings[gid]; g != nil {
		g.participants = removePID(g.participants, conn.PID)
		if len(g.participants) == 0 {
			delete(m.gatherings, gid)
		} else {
			g.session.NumParticipants = uint32(len(g.participants))
		}
	}
	m.mu.Unlock()
}

// makeCode assigns a deterministic 5-char private-room code to a gid.
func (m *Matchmaking) makeCode(gid uint32) string {
	const alphabet = "ABCDEFGHIJKLMNPQRSTUVWXYZ23456789"
	m.codeRand = m.codeRand*1103515245 + 12345 + gid
	x := m.codeRand
	b := make([]byte, 5)
	for i := range b {
		b[i] = alphabet[x%uint32(len(alphabet))]
		x /= uint32(len(alphabet))
	}
	code := string(b)
	m.byCode[code] = gid
	if g := m.gatherings[gid]; g != nil {
		g.code = code
	}
	return code
}

func containsPID(list []uint64, pid uint64) bool {
	for _, p := range list {
		if p == pid {
			return true
		}
	}
	return false
}

func removePID(list []uint64, pid uint64) []uint64 {
	out := make([]uint64, 0, len(list))
	for _, p := range list {
		if p != pid {
			out = append(out, p)
		}
	}
	return out
}

// TraceCall is an optional per-PID call trace hook. The core keeps no history of its
// own; a game server that wants one records calls here. No-op by default (the ring is
// only allocated on first use), so titles that never call it pay nothing.
func (m *Matchmaking) TraceCall(pid uint64, proto uint16, method uint32, callID uint32, body []byte) {
	_ = pid
	_ = proto
	_ = method
	_ = callID
	_ = body
}

// GatheringIDByPID returns the id of the gathering the given PID is in.
// Titles that publish "which room is my friend in" need the id itself, not just
// the participant list. Must NOT be called while holding m.mu.
func (m *Matchmaking) GatheringIDByPID(pid uint64) (uint32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.gatherings {
		if g == nil || g.session == nil {
			continue
		}
		for _, p := range g.participants {
			if p == pid {
				return g.session.ID, true
			}
		}
	}
	return 0, false
}

// SessionByPID returns the participant PID list of the gathering the given PID is in,
// or nil when it is in none. Must NOT be called while holding m.mu.
func (m *Matchmaking) SessionByPID(pid uint64) []uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.gatherings {
		if g == nil || g.session == nil {
			continue
		}
		for _, p := range g.participants {
			if p == pid {
				out := make([]uint64, len(g.participants))
				copy(out, g.participants)
				return out
			}
		}
	}
	return nil
}

// isTournamentIDList reconnaît la requête « donne-moi ces tournois » de Mario
// Kart : u32 nombre, suivi d'exactement autant d'identifiants sur 32 bits (la
// capture en montre 85, soit 344 octets). La sonde SSBU qui partage ce numéro
// de méthode n'a pas cette forme, d'où un tri fiable sans connaître le jeu.
func isTournamentIDList(body []byte) bool {
	if len(body) < 8 || len(body)%4 != 0 {
		return false
	}
	n := uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24
	return n > 0 && n <= 4096 && int(n)*4+4 == len(body)
}
