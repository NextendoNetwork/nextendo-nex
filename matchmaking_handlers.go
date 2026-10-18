package nex

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Matchmaking protocols and the methods MK8 uses.
const (
	ProtocolMatchMaking        uint16 = 0x15
	ProtocolMatchmakeExtension uint16 = 0x6D
	ProtocolMatchMakingExt     uint16 = 0x32

	// MatchMaking (0x15)
	MethodUnregisterGathering     uint32 = 0x02
	MethodFindBySingleID15        uint32 = 0x15
	MethodUpdateSessionURL        uint32 = 0x1B
	MethodUpdateSessionHostV1     uint32 = 0x28
	MethodGetDetailedParticipants uint32 = 0x0F
	MethodGetSessionURLs          uint32 = 0x29
	MethodUpdateSessionHost       uint32 = 0x2A

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
	// occurred": measured by measuring both servers with the same client, this was the LAST
	// call before the error, and the proven server answers it and carries on.
	MethodCloseParticipation              uint32 = 0x01
	MethodOpenParticipation               uint32 = 0x02
	MethodCreateMatchmakeSessionWithParam uint32 = 0x26
	MethodJoinMatchmakeSessionWithParam   uint32 = 0x27
	MethodAutoMatchmakeWithParamPostpone  uint32 = 0x28

	// UpdateApplicationBuffer(gid, buffer) is how a HOST publishes its match configuration
	// into the gathering — Smash sends ~422 bytes of it as soon as a lobby forms. Joiners
	// read it back off the session, so refusing it leaves them with a session they cannot
	// interpret and the lobby loops back to matchmaking (2618-0006). The the previous stack stack
	// implemented it
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
	MethodFindByParticipant uint32 = 0x33 // FindMatchmakeSessionByParticipant (Smash : arenes d amis)
	MethodBrowseNoHolder    uint32 = 0x34 // BrowseMatchmakeSessionNoHolderNoResultRange (public arena search)
	MethodSSBUArenaCode     uint32 = 0x37 // arena code create (same struct as MK8 0x44)
	MethodSSBUArenaResolve  uint32 = 0x38 // resolve arena code → bare u32 gid (like 0x45)
	MethodFindByGidList     uint32 = 0x30 // FindMatchmakeSessionByGatheringID (list<gid> → list<session>)
	MethodSSBUPreMatch      uint32 = 0x3A // pre-AutoMatchmake probe → bare u32 = 2 (cle de partition, voir le cas 0x3A)
	// Tournois Mario Kart 8, relevés sur la measured du serveur de Nintendo :
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
	// arene : creee par CreateMatchmakeSessionWithParam (0x6D.0x26), c est-a-dire une arene
	// Smash et non un salon de Partie rapide (0x6D.0x28). Seules celles-la peuvent sortir
	// dans la liste des arenes d amis (matchmaking_arenes_amis.go).
	arene bool
	// cree : creation du salon (attente d'un hote, PaliersAppariement).
	cree time.Time
	// orphelin : proprietaire deconnecte, salon garde pour la migration d'hote
	// (matchmaking_hote.go) ; ni appariement, ni liste publique, ni arenes d'amis tant que le
	// successeur n'a pas appele UpdateSessionHost (ses URL de session n'existent pas encore).
	orphelin bool
}

// Matchmaking is the in-memory matchmaking store + handlers, replicating the
// proven server's session lifecycle (create/join, participant tracking) and the
// server→client Participate notification the console's Pia waits on.
type Matchmaking struct {
	// FindByParticipantEnabled fait répondre RÉELLEMENT à FindMatchmakeSessionByParticipant
	// (0x6D.0x33) au lieu d'une liste vide. Animal Crossing s'en sert pour la visite d'île
	// entre amis. Smash a sa propre variante (arenes d amis, SansArenesAmis plus bas) ; le
	// défaut (false) laisse les autres jeux sur la liste vide.
	FindByParticipantEnabled bool
	// SansArenesAmis coupe la variante Smash de FindMatchmakeSessionByParticipant (arenes
	// d amis, matchmaking_arenes_amis.go). Elle s applique par defaut aux seules connexions
	// qui portent la cle d acces de Smash ; le serveur Smash n a rien a poser pour l avoir.
	SansArenesAmis bool
	// FindByParticipantVideVeutDireToutes : une liste de PID VIDE dans
	// FindMatchmakeSessionByParticipant signifie « n'importe quelle session ouverte »
	// et non « aucune ».
	//
	// MESURE DU 2026-08-24, SMM2 en cooperatif : le jeu envoie `pids=[]`. L'ancienne
	// implementation bouclait sur cette liste, donc elle rendait zero session quoi qu'il
	// arrive — deux joueurs entrant EN MEME TEMPS creaient chacun sa salle et ne se
	// voyaient jamais. Le drapeau FindByParticipantEnabled ne pouvait rien y changer :
	// il n'y avait rien a parcourir.
	//
	// C'est la meme convention que le reste de ce protocole, rencontree deux fois le
	// meme soir : liste vide = joker. Dans GetUsers elle veut dire « mon profil », dans
	// SearchCoursesPostedBy « mes niveaux », ici « toutes celles ou je peux entrer ».
	//
	// Le drapeau est separe parce qu'Animal Crossing active FindByParticipantEnabled
	// pour la visite d'ile ENTRE AMIS : il envoie de vrais PID, et lui rendre un jour
	// une liste ouverte ferait atterrir un visiteur sur l'ile d'un inconnu.
	FindByParticipantVideVeutDireToutes bool
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
	// nombre de participants AVANT l'ajout du visiteur (comme the previous stack) au lieu d'après. La Pia
	// d'Animal Crossing dimensionne son maillage P2P sur ce nombre : compté inclus, le visiteur
	// attend une « autre console » fantôme et échoue (2618-0502). Défaut false : MK8 (AutoMatchmake)
	// et SSBU gardent le comptage après-ajout avec lequel ils fonctionnent.
	JoinRespExistingCount bool
	// AutoSansSessionProtegee : AutoMatchmakeWithParam_Postpone (0x6D.0x28) n'entre JAMAIS
	// dans une session protegee par mot de passe dont l'appelant n'est pas deja participant ;
	// il ouvre alors sa propre session, comme si aucune n'etait compatible. Pour SSBU : une
	// arene se rejoint par 0x6D.0x27 (mot de passe pose par 0xd0600, seul appelant 0x3708b78,
	// JoinSessionSetting+0x18), jamais par l'appariement. Defaut false : un titre qui se
	// retrouve en prive par 0x28 et un mot de passe commun (PAC-MAN 99, 82000) garde ce
	// chemin, freine comme 0x27 (autoMdpCompatible, mdp_essais.go).
	AutoSansSessionProtegee bool
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

	notif *notifStore
	mu    sync.Mutex
	// OnSessionReady, s il est pose, est appele apres chaque AutoMatchmake reussi avec
	// le salon et ses participants. Sert a PAC-MAN 99 pour envoyer la passation Eagle ;
	// nil — le defaut — laisse tous les autres titres inchanges.
	OnSessionReady func(gid uint32, participants []uint64)

	// NotifierDeparts previent le PROPRIETAIRE du salon quand un participant s'en va.
	//
	// Sans cet avis, sa liste de joueurs ne DIMINUE jamais : elle accumule tous ceux qui
	// sont partis sans se deconnecter proprement. MESURE DU 2026-08-26 : dans SUPER MARIO
	// BROS. 35, l'hote annoncait DOUZE joueurs a un salon ou six consoles seulement
	// atteignaient le relais — les autres attendaient six absents et abandonnaient.
	//
	// Opt-in : nil chez les autres titres, qui tournent tres bien sans depuis des mois et
	// qu'on ne touche pas a cette heure-ci.
	NotifierDeparts bool

	// SortieProprietaire: when a gathering's owner LEAVES it (EndParticipation) while others
	// remain, a remaining participant takes it over (flag 0x10) or it is deleted, like the
	// original service (migrerSalonApresSortie). Opt-in: other titles keep the old behaviour
	// (the gathering keeps its departed owner).
	SortieProprietaire bool

	// Endpoint sert a joindre le proprietaire d'un salon depuis RemovePlayer, qui ne
	// recoit qu'un PID. Pose par le serveur de jeu en meme temps que NotifierDeparts.
	Endpoint *Endpoint

	gatherings map[uint32]*gathering
	byCode     map[string]uint32
	nextGID    uint32
	codeRand   uint32
	// mdpSeaux : refus de mot de passe d'arene par PID et par IP (mdp_essais.go), sous mu.
	mdpSeaux map[string]*mdpSeau
	// mdpPurgeA / mdpPurges : derniere purge des seaux vides et nombre de purges (mdpSeauDe).
	mdpPurgeA time.Time
	mdpPurges int

	// Appariement rend, pour une connexion, les paliers de l'appariement fidele de 0x6D.0x28
	// (matchmaking_criteres.go) ; nil (fonction ou resultat) : appariement d'avant, sur le seul
	// GameMode. Pose par le serveur de jeu.
	Appariement func(conn *Connection) *PaliersAppariement
	// JournalAppariement : journaliser les criteres reels de 0x6D.0x28 et 0x6D.0x34 et le
	// contenu de 0x6D.0x2C pour cette connexion (PID de test). nil : rien.
	JournalAppariement func(conn *Connection) bool
	// series : debut de la serie de recherches 0x6D.0x28 de chaque PID (Appariement), sous mu.
	series map[uint64]time.Time

	// MigrationHote : UpdateSessionHost (0x15.0x2A) applique et annonce (110000) quand il est
	// vrai pour le PID qui l'appelle, et un salon dont le proprietaire se deconnecte reste
	// ouvert a ceux qui restent quand il est vrai pour l'un d'eux (matchmaking_hote.go). nil :
	// comportement d'avant (acquittement sans effet, salon supprime).
	MigrationHote func(pid uint64) bool
	// SessionPartSmash : UpdateMatchmakeSessionPart (0x6D.0x2C) applique selon les drapeaux de
	// Smash (matchmaking_maj.go) pour cette connexion. nil ou faux : comportement d'avant.
	SessionPartSmash func(conn *Connection) bool
	// FiltreParcours : la recherche publique d'arenes (0x6D.0x34 sans mot de code) applique les
	// criteres envoyes (GameMode, attributs, places, verrou ; matchmaking_criteres.go) pour cette
	// connexion. nil ou faux : comportement d'avant (toute session ouverte).
	//
	// Format prouve (vecteurs critere_arene_*.hex : genre 7, attributs 1 a 5 = filtres
	// 0x5306f18..0x5306f1c poses par 0x168b240, vides sans filtre). Correspondance avec les
	// attributs d'une arene creee, relevee dans le code du jeu :
	//   - memes emplacements : la creation (0x168b150-0x168b194) et la recherche
	//     (0x168b3a0-0x168b41c) ecrivent les attributs 1 a 5 aux memes decalages +0x5c, +0x6c,
	//     +0x7c, +0x8c, +0x9c du reglage Pia ;
	//   - memes valeurs : le menu des reglages (0x1c7f7c0) traduit les deux en lignes de menu.
	//     Attribut 1 : creation 0x53069b0 de 1 a 7 (0x1c7fcd4), filtre 0x5306f18 de 0 a 7
	//     (0x1c81d24) ; attribut 2 : creation 0x53069b2 de 1 a 4 (0x1c7fe14), filtre 0x5306f19
	//     de 0 a 4 (0x1c81df4) ; attribut 3 : creation 0x5306e55 de 0 a 2 ecrit plus 1, donc de
	//     1 a 3 (0x168b130-0x168b148), filtre 0x5306f1a de 0 a 3 (0x1c81e28) ; attributs 4 et
	//     5 : creation 0x5306e8f et 0x5306e90, filtres 0x5306f1b et 0x5306f1c, valeurs 1 ou 2
	//     avec les memes lignes de menu (chaines 0x4545280 et 0x4545281 : 0x1c81fb0, 0x1c81ff8,
	//     0x1c81e5c, 0x1c81ea4). Le filtre 0 est « peu importe » (champ vide, vecteur
	//     critere_arene_sans_filtre) ; l'attribut 3 de la creation est decale de 1 pour ne
	//     jamais valoir 0.
	// Reste hypothese la regle de comparaison du serveur de Nintendo (egalite NEX), d'ou
	// l'interrupteur. Genre 8 (arenes d'amis) : critereRefus.
	FiltreParcours func(conn *Connection) bool
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
			// Réponse réelle là où le jeu s'en sert (Animal Crossing, puis Smash) ; ailleurs
			// on conserve la liste vide.
			if m.FindByParticipantEnabled {
				return m.findByParticipant(conn, req)
			}
			// Smash : liste des arenes d amis (matchmaking_arenes_amis.go). Le jeu appelle
			// 0x33 depuis le menu des arenes, pas seulement au demarrage.
			if m.arenesAmisActives(conn) {
				return m.findArenesAmis(conn, req)
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
			// On distingue par la forme de la requête, mesurée sur measured : MK8
			// envoie u32 nombre + nombre × u32, donc exactement 4+4n octets.
			if isTournamentIDList(req.Body) {
				return m.tournamentsByIDs(conn, req)
			}
			// CE QUE C'EST POUR SMASH (13.0.5). Requete : u32 = le genre de matchmaking
			// (0x16a76b8 / 0x16a8e30 : [x21], ou 0x530779c pour le genre 13), envoye par
			// TaskMatchmake (0x17086f0 -> 0x172c190 -> 0x252300 -> 0x252330, methode 0x3a en
			// 0x252420). Reponse : u32 lu par 0x253f30, range par le rappel 0x1702470 en
			// objet+0xb0 (0x1702530-0x1702534), puis recopie tel quel en ATTRIBUT 0 de la
			// session creee et du critere de recherche (exact) pour les genres 1, 2, 3, 5, 7 et
			// 13 (masque 0x20ae : 0x1689b6c-0x1689b8c, 0x16969d4-0x16969fc). Le jeu le
			// redemande apres une heure (0x16a8e1c-0x16a8e2c : 0x34630b89fff ns). C'est donc
			// une cle de partition : seuls les joueurs qui ont recu la meme valeur se
			// rencontrent. Le sens que Nintendo lui donnait (version, periode) n'est pas
			// observable (hypothese). La valeur 2 vient d'un ancien commentaire de ce coeur
			// (« la console reelle recevait u32=2 », 967c902), sans capture conservee :
			// sa provenance est une hypothese. Elle importe peu : seule compte une valeur
			// IDENTIQUE pour tous (le critere demande l'attribut 0 exact, 0x2559a0), qui garde tous
			// les joueurs dans la meme partition ; c'est ce que fait la constante.
			if m.JournalAppariement != nil && m.JournalAppariement(conn) {
				fmt.Printf("[MM] 0x3a pid=%d genre=%d -> 2\n", conn.PID, NewStreamIn(req.Body, conn.Settings).U32())
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
		case MethodGetDetailedParticipants:
			return m.getDetailedParticipants(conn, req)
		case MethodUnregisterGathering:
			m.unregister(conn, req)
			return boolResponse(conn, ProtocolMatchMaking, req, true)
		case MethodUpdateSessionURL:
			return boolResponse(conn, ProtocolMatchMaking, req, true)
		case MethodUpdateSessionHost:
			return m.updateSessionHost(conn, req)
		case MethodUpdateSessionHostV1:
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
// an empty list (what the game server did before) makes every friend-join fail because the
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
	// Le cadenas est pose par le SERVEUR. Smash met le mot de passe de l'arene dans
	// UserPassword (Pia 0xe34a0-0xe3554, depuis CreateSessionSetting+0x2a0 rempli par
	// 0xc1340, appele en 0x37078e4) mais n'ecrit jamais UserPasswordEnabled : sans cette
	// ligne une arene protegee etait vue comme ouverte par la liste, l'appariement et la
	// jonction. Un drapeau pose par le client sans mot de passe donne une session que seul
	// son proprietaire peut rejoindre (mot de passe vide jamais valable).
	session.UserPasswordEnabled = session.UserPasswordEnabled || session.UserPassword != ""
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

// sessionsSeulMax : sessions dont un PID est le SEUL participant, au plus, par PID.
//
// Relecture r2 : chaque 0x6D.0x28 sans candidat compatible (et chaque 0x6D.0x26) ouvrait une
// session de plus, sans borne tant que la connexion restait ouverte (RemovePlayer ne nettoie
// qu'au depart) : 7391 sessions au nom d'un seul client pendant le test de force brute, et
// chacune garde sa MatchmakeSession entiere (attributs, ApplicationBuffer, parametres) dans
// un conteneur a memoire bornee. Une console ne tient qu'une session Pia a la fois et retourne a
// l'appariement depuis la session precedente (hypothese : une session ou elle est encore
// seule est abandonnee, personne d'autre n'y attend). Au-dela de sessionsSeulMax, les plus
// anciennes (gid le plus bas) sont fermees. Seuls les participants ACTUELS comptent : une
// session ou quelqu'un d'autre est present n'est jamais touchee, mais une session dont les
// autres sont repartis compte comme « seule » et peut etre fermee sans avis (personne d'autre
// n'y est plus a prevenir). C'est voulu : retenir « quelqu'un est deja entre » laisserait deux
// comptes complices ouvrir des sessions sans borne (l'un cree, l'autre entre puis repart).
const sessionsSeulMax = 16

// borneSessionsSeul (sous m.mu) : laisse au plus sessionsSeulMax-1 sessions ou pid est seul,
// avant que createGathering n'en ouvre une de plus. Rend le nombre de sessions fermees.
func (m *Matchmaking) borneSessionsSeul(pid uint64) int {
	var seul []uint32
	for gid, g := range m.gatherings {
		if g != nil && len(g.participants) == 1 && g.participants[0] == pid {
			seul = append(seul, gid)
		}
	}
	if len(seul) < sessionsSeulMax {
		return 0
	}
	sort.Slice(seul, func(i, j int) bool { return seul[i] < seul[j] })
	trop := seul[:len(seul)-sessionsSeulMax+1]
	for _, gid := range trop {
		if g := m.gatherings[gid]; g != nil && g.code != "" {
			delete(m.byCode, g.code)
		}
		delete(m.gatherings, gid)
	}
	return len(trop)
}

// createGathering registers a new gathering owned by the caller (mutex held).
func (m *Matchmaking) createGathering(conn *Connection, src *MatchmakeSession) *gathering {
	m.borneSessionsSeul(conn.PID)
	gid := m.nextGID
	m.nextGID++
	session := *src
	finalizeCreatedSession(&session, gid, conn.PID)
	g := &gathering{session: &session, participants: []uint64{conn.PID}, hostConnID: conn.ID, cree: time.Now()}
	m.gatherings[gid] = g
	return g
}

// notifyJoin pushes the Participate (3001) notification to the gathering owner
// when a new player (not the owner) joins — the event Pia's WaitNotification
// blocks on. The joiner is never notified of its own join.
// notifyParticipation replicates the proven server's join notifications, taken
// byte-for-byte from a live 2-player measured:
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

	// Le salon vient de bouger : PAC-MAN 99 attend ici l'adresse du relais Eagle. Hors de
	// ce titre le rappel est nil et rien ne change.
	if m.OnSessionReady != nil {
		m.OnSessionReady(gid, participants)
	}
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

// paireStricteHorsHote dit si l'arrivant forme une paire NAT strict (natm 1 contre natm 2,
// classes Pia 0515/0516, classePia) avec un membre du salon AUTRE que l'hote.
//
// Seule la paire (arrivant, hote) peut passer par le relais : l'arrivant recoit les URL de
// session de l'hote (relayedFor) et l'adresse des autres membres par l'hote, via Pia, que
// le serveur ne peut pas reecrire (pushInitiateProbe ne repointe que les paires de
// SessionRelayee). Une paire 0515/0516 entre l'arrivant et un autre membre se perce donc
// toujours en direct, et c'est elle qui echoue : prod du 17/10 00:30Z a 03:40Z, sur 53
// echecs directs 0515/0516, 11 entre membres non hotes de salons de Partie rapide a 4
// (autoMatchmake), 21 dans des arenes rejointes par 0x6D.0x27 (choix du joueur, hors de
// portee d'autoMatchmake), le reste avec l'hote. natm inconnu (0) d'un cote : pas de paire
// stricte (meme regle que classePia, « ? »).
func paireStricteHorsHote(ep *Endpoint, arrivant *Connection, cand *gathering) bool {
	if ep == nil || arrivant == nil || cand == nil || cand.session == nil {
		return false
	}
	a := natmStation(arrivant)
	if a != 1 && a != 2 {
		return false
	}
	hote := cand.session.HostPID
	if c := ep.FindConnectionByID(cand.hostConnID); c != nil {
		hote = c.PID
	}
	for _, pid := range cand.participants {
		if pid == arrivant.PID || pid == hote {
			continue
		}
		c := ep.FindConnectionByPID(pid)
		if c == nil {
			continue
		}
		if b := natmStation(c); (a == 1 && b == 2) || (a == 2 && b == 1) {
			return true
		}
	}

	return false
}

// motDePasseCompatible dit si un salon protege par mot de passe peut accueillir ce joueur.
//
// CE QU'ON CORRIGE. Le filtre disait simplement « pas de mot de passe », ce qui est juste
// pour l'appariement PUBLIC — personne ne doit atterrir par hasard dans un salon prive.
// Mais il s'appliquait AUSSI a un joueur qui demande explicitement le mode prive : sa
// recherche ne pouvait alors rien trouver, jamais, et il ouvrait son propre salon.
//
// MESURE, PAC-MAN 99, 2026-08-26 : mode public 92000, 25 jointures sur 85 et des salons a
// 2, 3, 4 joueurs. Mode prive 82000, ZERO jointure sur 11 et count=1 a chaque fois. Deux
// personnes ne se sont jamais rencontrees en prive. Le filtre NAT est hors de cause : pas
// une seule ligne « NAT-aware » dans tout le journal.
//
// La regle : un salon sans mot de passe reste ouvert a tous. Un salon protege n'accepte que
// celui qui presente LE MEME mot de passe, non vide. Exiger qu'il soit non vide est
// deliberé — sinon deux salons prives distincts, tous deux « proteges » par une chaine
// vide, se confondraient, ce qui est exactement ce qu'un salon prive ne doit pas faire.
//
// Comparaison a temps constant : le mot de passe vient du reseau et distingue deux salons.
//
// Le drapeau UserPasswordEnabled de l'ARRIVANT n'est plus exige : Pia ne l'ecrit jamais (aucune
// ecriture de MatchmakeSession+0x124 dans le main 13.0.5 hors des copies 0x254ad8 et du
// constructeur 0x1e2674), seul le mot de passe qu'il porte compte.
func motDePasseCompatible(cand *MatchmakeSession, src *MatchmakeSession) bool {
	if !cand.UserPasswordEnabled {
		return true
	}
	return motDePasseValide(cand.UserPassword, src.UserPassword)
}

// motDePasseValide compare le mot de passe presente a celui d'une session protegee.
//
// Un mot de passe vide n'est JAMAIS valable, d'un cote comme de l'autre. La comparaison porte
// sur les empreintes SHA-256 des deux chaines : subtle.ConstantTimeCompare rend tout de suite
// sur deux longueurs differentes, comparer des empreintes de taille fixe ne laisse donc pas
// mesurer la longueur du mot de passe attendu.
func motDePasseValide(attendu, presente string) bool {
	if attendu == "" || presente == "" {
		return false
	}
	a := sha256.Sum256([]byte(attendu))
	p := sha256.Sum256([]byte(presente))
	return subtle.ConstantTimeCompare(a[:], p[:]) == 1
}

// sessionProtegee dit si une session exige un mot de passe pour y entrer.
func sessionProtegee(s *MatchmakeSession) bool { return s.UserPasswordEnabled }

// expurgerSessionProtegee retire d'une COPIE de session ce qu'un simple curieux ne doit pas
// lire : jamais le mot de passe, et la cle de session quand la session est protegee (elle
// suffit a la Pia pour parler au salon). Le cadenas (UserPasswordEnabled) reste : Smash le
// lit pour afficher l'arene comme protegee (0xd91f0 puis 0x3706c98).
func expurgerSessionProtegee(r *MatchmakeSession) {
	r.UserPassword = ""
	if sessionProtegee(r) {
		r.SessionKey = nil
	}
}

// ModeIsole dit si un GameMode est celui d'une session isolee par le serveur de jeu : un
// match de tournoi SSBU (mot de code « T-tournoi-match ») recoit le GameMode
// 0x54000000 | 24 bits de hachage (super-smash-bros-ultimate, tournoi_matchmaking.go,
// tournoiModeIsole). Une telle session est la SEULE ou ses joueurs peuvent se retrouver.
func ModeIsole(mode uint32) bool { return mode&0xFF000000 == 0x54000000 }

// autoMdp : etat du mot de passe d'une requete 0x6D.0x28 pendant le parcours des candidats.
// permis : -1 frein pas encore consulte, 0 freine, 1 comparaison permise. echecs : sessions
// protegees comparees sans succes.
type autoMdp struct {
	permis int
	echecs int
}

// autoMdpCompatible (sous m.mu) : la session protegee cand peut-elle accueillir l'appelant de
// 0x6D.0x28 (src = session de sa requete).
//
// CE QU'ON CORRIGE (relecture r2). 0x28 comparait le mot de passe de la requete a celui de
// chaque session protegee du meme GameMode (public : 0x34 rend les arenes protegees avec leur
// GameMode) et faisait entrer l'appelant sur le bon, sans passer par le frein de 0x27 :
// « 7391 » trouve en 7392 essais (896 ms), cle de session de l'arene rendue, frein de 0x27
// deja epuise.
//
// Maintenant : un participant deja present garde la regle d'avant (rien a deviner). Sinon,
// AutoSansSessionProtegee : jamais ; mot de passe vide : jamais (comme avant) ; frein atteint
// (mdpEssaiPermis, memes seaux que 0x27, consulte une fois par requete) : jamais, sans
// comparaison ; sinon comparaison a temps constant, et un echec compte (autoMatchmake note
// un seul refus par requete, mdpNoterRefus).
func (m *Matchmaking) autoMdpCompatible(conn *Connection, cand *gathering, src *MatchmakeSession, etat *autoMdp) bool {
	if containsPID(cand.participants, conn.PID) {
		return motDePasseCompatible(cand.session, src)
	}
	if m.AutoSansSessionProtegee || src.UserPassword == "" {
		return false
	}
	if etat.permis < 0 {
		etat.permis = 0
		if m.mdpEssaiPermis(conn) {
			etat.permis = 1
		}
	}
	if etat.permis == 0 {
		return false
	}
	if motDePasseCompatible(cand.session, src) {
		return true
	}
	etat.echecs++
	return false
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

	// Appariement fidele (matchmaking_criteres.go) : la requete lue en entier, criteres compris,
	// seulement pour une connexion qui a l'appariement fidele ou le journal. Requete incomplete
	// (lecture stricte), criteres hors bornes (criteresBornes : evalues sous m.mu pour chaque
	// salon) ou session isolee (match de tournoi) : comme avant.
	var paliers *PaliersAppariement
	if m.Appariement != nil && !ModeIsole(src.GameMode) {
		paliers = m.Appariement(conn)
	}
	journalCriteres := m.JournalAppariement != nil && m.JournalAppariement(conn)
	var complet AutoMatchmakeParamComplet
	completLu, bornes := false, false
	if paliers != nil || journalCriteres {
		complet, completLu = lireAutoMatchmakeComplet(req.Body, s)
		bornes = completLu && criteresBornes(complet.Criteres)
	}
	if !bornes {
		paliers = nil
	}
	if journalCriteres {
		fmt.Printf("[MM] criteres 0x28 pid=%d lu=%v bornes=%v session: mode=%d minP=%d maxP=%d attribs=%s param=%s ; %d critere(s): %s\n",
			conn.PID, completLu, bornes, src.GameMode, src.MinParticipants, src.MaxParticipants, texteAttribs(src.Attribs),
			texteParam(src.Param.Params), len(complet.Criteres), texteCriteres(complet.Criteres))
	}

	// [Nextendo] NAT-aware: the joiner's own class, and the endpoint used to look up members.
	ep := conn.Endpoint
	joinerClass := classifyNAT(conn.NATBehaviour())

	m.mu.Lock()
	// Paliers (PaliersAppariement) : attente du joueur depuis le debut de sa serie de recherches,
	// palier/ecart/attente du salon retenu dans chaque categorie, salons refuses par palier (pris
	// faute de mieux, sauf refus strict) et nombre de salons evalues.
	now := time.Now()
	var debutSerie time.Time
	if paliers != nil {
		debutSerie = m.debutSerie(conn.PID, now)
	}
	type evaluation struct {
		refuse bool // palier pas encore permis (PaliersAppariement.permis)
		palier int
		ecart  uint32
		age    time.Duration
	}
	mieux := func(a, b evaluation) bool {
		if a.refuse != b.refuse {
			return !a.refuse
		}
		if a.palier != b.palier {
			return a.palier < b.palier
		}
		if a.ecart != b.ecart {
			return a.ecart < b.ecart
		}
		return a.age > b.age
	}
	var evG, evRepli, evStrict evaluation
	var refuses [4]int
	candidats := 0
	var g *gathering
	var repli *gathering // salon dont l'hote est momentanement absent : accepte en dernier recours
	// salon dont un membre hors hote forme une paire NAT strict avec l'arrivant (paireStricteHorsHote)
	var repliStrict *gathering
	evitesStricts := 0
	skippedNAT := 0
	// [Nextendo] A solo-isolated PID (NEX_SOLO_PIDS) never joins an existing lobby: it always
	// opens its own gathering. Gives a private test account a reliable solo online lobby on a
	// SHARED server, sidestepping the P2P/NAT hole-punch that fails (2618-0521) when it gets
	// matched with real, unreachable consoles.
	soloForced := isSoloPID(conn.PID)
	// Sessions protegees : mot de passe compare au plus une fois par candidat, sous le frein des
	// essais de 0x6D.0x27 (autoMdpCompatible). gParMdp / repliParMdp / repliStrictParMdp : la
	// session retenue (ou le repli) a ete ouverte par le mot de passe de la requete.
	mdp := autoMdp{permis: -1}
	gParMdp, repliParMdp, repliStrictParMdp := false, false, false
	if !soloForced {
		for _, cand := range m.gatherings {
			if cand.session.GameMode == src.GameMode &&
				cand.session.OpenParticipation && !cand.orphelin &&
				uint16(len(cand.participants)) < cand.session.MaxParticipants {
				parMdp := false
				if sessionProtegee(cand.session) {
					if !m.autoMdpCompatible(conn, cand, src, &mdp) {
						continue
					}
					parMdp = true
				}
				// Skip a lobby the joiner can't hole-punch with (would give everyone 2618-0510);
				// keep looking for a compatible one.
				//
				// [Nextendo] Sauf pour une session isolee (ModeIsole, match de tournoi SSBU) :
				// il n'y a pas d'autre salon pour ces joueurs. La sauter scindait le match en
				// deux salons, chacun attendant seul un adversaire qui ne vient jamais (etat 7
				// du client, 0x16c5a54-0x16c5bdc : il n'attend que les joueurs du match), alors
				// qu'un percage tente garde ses chances (et le relais par paire s'il est arme).
				if !ModeIsole(src.GameMode) && !gatheringNATCompatible(ep, joinerClass, cand.participants, conn.PID) {
					skippedNAT++
					continue
				}
				// Appariement fidele : palier du salon ; un palier que l'attente ne permet pas encore
				// classe le salon apres tous les salons permis, sans l'ecarter (PaliersAppariement).
				var ev evaluation
				if paliers != nil {
					candidats++
					ev.age = now.Sub(cand.cree)
					if a := now.Sub(debutSerie); a > ev.age {
						ev.age = a
					}
					ev.palier = paliers.palier(complet.Criteres, src, cand.session, len(cand.participants))
					if !paliers.permis(ev.palier, ev.age) {
						ev.refuse = true
						refuses[ev.palier]++
					}
					ev.ecart = ecartNiveau(src, cand.session, paliers.AttributNiveau)
				}
				// [Nextendo] PREFERER un salon dont l'hote est en ligne.
				//
				// Un joueur deconnecte garde sa place quelques secondes (voir le delai de
				// grace cote jeu), le temps que sa console rouvre sa connexion. Pendant ce
				// delai son salon reste joignable, et un arrivant pouvait y entrer pour
				// attendre quelqu'un qui ne revenait pas : la partie ne demarrait qu'au
				// DEUXIEME essai, apres expiration du delai.
				//
				// C'est une PREFERENCE et non un refus. Exiger un hote connecte remettrait
				// chacun dans son propre salon des que l'hote cligne des yeux, ce qui est
				// exactement la fragmentation qu'on vient de corriger : tout le monde
				// cherche en meme temps et personne ne se rencontre. On garde donc le salon
				// a hote absent comme repli, utilise seulement si c'est le seul disponible.
				if ep != nil && ep.FindConnectionByPID(cand.session.OwnerPID) == nil {
					if repli == nil || (paliers != nil && mieux(ev, evRepli)) {
						repli, repliParMdp, evRepli = cand, parMdp, ev
					}

					continue
				}
				// [Nextendo] PREFERER un salon sans paire NAT strict hors de l'hote
				// (paireStricteHorsHote) : celle-la ne passe jamais par le relais. Une
				// preference encore, jamais un refus : faute d'autre salon, celui-ci est
				// pris (repliStrict), avant un salon a hote absent (hypothese : une paire
				// 0515/0516 echoue en direct dans 35 a 57 % des cas, commentaire de
				// besoinSeuilStrict, alors qu'un hote absent fait rater le premier essai).
				// Pas pour une session isolee : il n'y en a pas d'autre. Pas non plus pour un
				// salon ou l'arrivant figure deja.
				if !ModeIsole(src.GameMode) && !containsPID(cand.participants, conn.PID) && paireStricteHorsHote(ep, conn, cand) {
					evitesStricts++
					if repliStrict == nil || (paliers != nil && mieux(ev, evStrict)) {
						repliStrict, repliStrictParMdp, evStrict = cand, parMdp, ev
					}

					continue
				}
				if paliers == nil {
					g, gParMdp = cand, parMdp
					break
				}
				// Appariement fidele : le meilleur salon permis, pas le premier.
				if g == nil || mieux(ev, evG) {
					g, gParMdp, evG = cand, parMdp, ev
				}
			}
		}
	}
	// Refus strict (PaliersAppariement.RefusDes) : un salon refuse n'est pas pris faute de mieux.
	// Chaque categorie a retenu un salon permis s'il y en avait un (mieux) : un salon retenu
	// refuse veut dire qu'aucun salon de sa categorie n'etait permis.
	if paliers != nil && paliers.refusStrict(candidats) {
		if g != nil && evG.refuse {
			g = nil
		}
		if repliStrict != nil && evStrict.refuse {
			repliStrict = nil
		}
		if repli != nil && evRepli.refuse {
			repli = nil
		}
	}
	evRetenu := evG
	if g == nil && repliStrict != nil {
		evRetenu = evStrict
	} else if g == nil && repli != nil {
		evRetenu = evRepli
	}
	if g != nil && evitesStricts > 0 {
		fmt.Printf("[MM] NAT strict : pid=%d natm=%d evite %d salon(s) ou un membre hors hote forme une paire 0515/0516 avec lui -> gid=%d\n",
			conn.PID, natmStation(conn), evitesStricts, g.session.ID)
	}
	if g == nil && repliStrict != nil {
		g, gParMdp = repliStrict, repliStrictParMdp
		fmt.Printf("[MM] NAT strict : pid=%d natm=%d, aucun autre salon : gid=%d garde malgre une paire 0515/0516 hors hote (directe, sans relais)\n",
			conn.PID, natmStation(conn), g.session.ID)
	}
	if g == nil && repli != nil {
		g, gParMdp = repli, repliParMdp
	}
	// Un refus de plus (un seul par requete) quand au moins une session protegee a ete
	// comparee en vain et que l'appelant n'entre dans aucune par son mot de passe.
	freinPlein := mdp.echecs > 0 && !gParMdp && m.mdpNoterRefus(conn)
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
	// Serie de recherches finie pour tout le salon, que l'arrivant ait les paliers ou non : un
	// PID qui a les paliers, rejoint par un joueur qui ne les a pas, gardait sinon sa serie, et
	// sa recherche suivante (dans les 10 min) avait tout de suite droit au palier 3.
	if count >= 2 && len(m.series) > 0 {
		m.finSeries(parts)
	}
	m.mu.Unlock()

	if paliers != nil {
		pal := -1 // salon cree
		if joined {
			pal = evRetenu.palier
		}
		fmt.Printf("[MM] appariement pid=%d maxP=%d niveau=%v -> gid=%d joined=%v palier=%d faute-de-mieux=%v ecart=%d attente=%s candidats=%d pas-encore-permis(p1=%d p2=%d p3=%d) serie=%s critere=%s\n",
			conn.PID, src.MaxParticipants, attributOuZero(src.Attribs, paliers.AttributNiveau), gid, joined, pal, joined && evRetenu.refuse, evRetenu.ecart,
			evRetenu.age.Round(time.Second), candidats, refuses[1], refuses[2], refuses[3], now.Sub(debutSerie).Round(time.Second), texteCriteresCourts(complet.Criteres))
	}

	if freinPlein {
		fmt.Printf("[MM] autoMatchmake pid=%d addr=%s : mot de passe refuse, essais suivants freines (1 par %v et par PID, 1 par %v et par IP)\n",
			conn.PID, conn.RemoteAddr, mdpFuitePID, mdpFuiteIP)
	}

	// Nouvelle session Pia pour l'appelant, hote ou visiteur : ses stations ne valent plus
	// rien jusqu'a son prochain ReplaceURL (natpret.go).
	conn.natRAZ("autoMatchmake")

	// La reponse brute porte le mot de passe d'une session protegee : pas dans le journal.
	vidage := out.Bytes()
	if sessionProtegee(&result) {
		vidage = nil
	}
	fmt.Printf("[MM] autoMatchmake pid=%d -> gid=%d owner=%d host=%d joined=%v count=%d mode=%d flags=%d state=%d minP=%d maxP=%d skLen=%d attribs=%v pwOn=%v pwLen=%d open=%v respLen=%d\n  resp=%x\n",
		conn.PID, gid, result.OwnerPID, result.HostPID, joined, count, result.GameMode, result.Flags, result.State, result.MinParticipants, result.MaxParticipants, len(result.SessionKey), result.Attribs, result.UserPasswordEnabled, len(result.UserPassword), result.OpenParticipation, len(out.Bytes()), vidage)
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
	g.arene = true
	result := *g.session
	gid := g.session.ID
	parts := append([]uint64(nil), g.participants...)
	out := NewStreamOut(s)
	out.Add(&result)
	m.mu.Unlock()

	conn.natRAZ("createSession")
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
	// JoinMatchmakeSessionParam : en-tete de structure, GID, puis le mot de passe saisi en
	// 6e champ (matchmaking.go, JoinMatchmakeSessionParam, serialiseur du jeu 0x252b30).
	param, motDePasseLu := lireParamRejoindre(req.Body, s)
	gid := param.GID

	m.mu.Lock()
	g := m.gatherings[gid]
	// Salon orphelin (matchmaking_hote.go) : il attend le successeur elu par les consoles, ses
	// URL de session n'existent pas encore. Un arrivant y recevrait des URL vides ; comme avant
	// la migration d'hote (le salon etait supprime), la jonction echoue. Un participant deja
	// present (nouvelle tentative) garde sa place.
	if g != nil && g.orphelin && !containsPID(g.participants, conn.PID) {
		m.mu.Unlock()
		fmt.Printf("[MM] joinSession pid=%d gid=%d : salon orphelin (migration d'hote en cours) -> refuse\n", conn.PID, gid)
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousSessionVoid)
	}
	if g == nil {
		m.mu.Unlock()
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousSessionVoid)
	}
	existing := len(g.participants) // participants DÉJÀ présents, AVANT d'ajouter l'appelant
	joined := false
	if !containsPID(g.participants, conn.PID) {
		// Arene protegee : le mot de passe saisi doit etre le bon. Ni le proprietaire ni
		// l'hote ne sont re-verifies (son arene, il la connait), ni un participant deja
		// present (ce bloc ne s'execute pas pour lui). Le refus porte le code que Smash
		// traduit en « Le code d'acces est incorrect. » (resultcodes.go) ; avant ce controle,
		// n'importe qui entrait dans n'importe quelle arene avec son seul identifiant.
		proprietaire := conn.PID == g.session.OwnerPID || conn.PID == g.session.HostPID
		if sessionProtegee(g.session) && !proprietaire {
			if !motDePasseLu {
				m.mu.Unlock()
				fmt.Printf("[MM] joinSession pid=%d gid=%d : arene protegee, requete illisible -> refus\n", conn.PID, gid)
				return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
			}
			// Essais bornes par PID et par IP (mdp_essais.go) : au-dela, refus sans comparaison
			// et sans ligne de journal, avec 0x800300C9 (« Impossible de rejoindre cette
			// arene. ») et non 0x800300D5 : le refus freine ne depend pas du mot de passe, le
			// joueur qui tape le bon ne doit pas lire « Le code d'acces est incorrect. ».
			if !m.mdpEssaiPermis(conn) {
				m.mu.Unlock()
				return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousInvalidGatheringPassword)
			}
			if !motDePasseValide(g.session.UserPassword, param.UserPassword) {
				plein := m.mdpNoterRefus(conn)
				m.mu.Unlock()
				if plein {
					fmt.Printf("[MM] joinSession pid=%d gid=%d addr=%s : mot de passe refuse, essais suivants freines (1 par %v et par PID, 1 par %v et par IP)\n",
						conn.PID, gid, conn.RemoteAddr, mdpFuitePID, mdpFuiteIP)
				} else {
					fmt.Printf("[MM] joinSession pid=%d gid=%d : mot de passe refuse\n", conn.PID, gid)
				}
				return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultRendezVousUserPasswordUnmatch)
			}
		}
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
	// La réponse au JOIN doit annoncer le nombre de participants tel que le serveur the previous stack de
	// référence le renvoie : le compte AVANT ajout du visiteur (juste l'hôte = 1), PAS après (2).
	// La Pia d'Animal Crossing bâtit son maillage P2P d'après ce nombre : compté inclus (2), le
	// visiteur attend une 2ᵉ « autre console » qui n'existe pas et échoue sur « one or more other
	// consoles are not responding » (2618-0502). Mesuré : the previous stack Join/39 = NumParticipants 1.
	if m.JoinRespExistingCount && joined {
		result.NumParticipants = uint32(existing)
	}
	parts := append([]uint64(nil), g.participants...)
	out := NewStreamOut(s)
	out.Add(&result)
	m.mu.Unlock()

	conn.natRAZ("joinSession")
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
	critereLu := false
	if len(req.Body) > 0 {
		in := NewStreamIn(req.Body, s)
		in.Extract(&criteria)
		critereLu = in.Err() == nil
		if !critereLu {
			criteria.Codeword = ""
			criteria.ExcludeUserPassword = true // illisible : on garde l'ancien filtre
		}
	}
	wantCode := criteria.Codeword
	// Filtres de la recherche d'arenes (FiltreParcours) : seulement pour la recherche publique
	// (sans mot de code : une recherche par code, tournoi ou Dodo Code, garde sa regle).
	filtrer := wantCode == "" && critereLu && m.FiltreParcours != nil && m.FiltreParcours(conn)
	journal := m.JournalAppariement != nil && m.JournalAppariement(conn)
	// Le filtre ne s'applique qu'a un critere lu en entier (lecture stricte : un contenu tronque
	// dans la Structure rendait des champs a zero sans erreur) et dans les bornes du jeu
	// (criteresBornes : il est evalue sous m.mu pour chaque salon). Sinon : liste d'avant.
	bornes := false
	if filtrer || journal {
		if c, ok := lireCritereStrict(req.Body, s); ok {
			bornes = criteresBornes([]MatchmakeSessionSearchCriteria{c})
		}
	}
	filtrer = filtrer && bornes
	filtrees := 0
	// raisons : salons ecartes par champ du critere (critereRefus : ensemble fini de noms).
	raisons := map[string]int{}
	// « Arenes avec code d'acces : Inclure / Ne pas inclure ». Le jeu porte ce choix dans
	// ExcludeUserPassword des criteres (Smash 0x3707c08 : bit 3 des options de recherche ->
	// Pia 0x1134a0 -> SetExcludeUserPassword 0x255b50, serialise en +0x100 par 0x255f00). Une
	// arene protegee est donc LISTEE, cadenas compris, sauf demande contraire : c'est ce
	// cadenas qui fait demander le code au joueur avant la jonction.
	exclureProtegees := criteria.ExcludeUserPassword || len(req.Body) == 0

	m.mu.Lock()
	var sessions []*MatchmakeSession
	for _, g := range m.gatherings {
		if !g.session.OpenParticipation || g.orphelin {
			continue
		}
		if wantCode != "" {
			// Recherche par code : le code EST le laissez-passer, on ne réapplique donc
			// pas le filtre « sans mot de passe » qui exclurait justement ces sessions.
			if g.session.Codeword != wantCode && g.code != wantCode {
				continue
			}
		} else if exclureProtegees && sessionProtegee(g.session) {
			continue
		}
		if filtrer {
			if raison := critereRefus(&criteria, g.session, len(g.participants), -1); raison != "" {
				filtrees++
				raisons[raison]++
				continue
			}
		}
		r := *g.session
		// Recherche par Dodo Code (ACNH, wantCode!="") : le demandeur détient le code, il est
		// donc autorisé à rejoindre → on GARDE la clé de session, comme the previous stack et comme
		// findByParticipant, sinon la visite cale (2618-0502). Recherche publique sans code
		// (arène Smash) : on la retire pour ne pas la divulguer à un simple scan. Celle d'une
		// session protegee par mot de passe ne sort jamais, code ou pas.
		// Une session isolee par le serveur de jeu (ModeIsole, match de tournoi SSBU) ne rend
		// jamais sa cle ici, mot de code ou pas : ses joueurs la recoivent de
		// AutoMatchmakeWithParam (le jeu rejoint son match par JoinRandomSession, 0x3707440 ->
		// 0x145910), et le mot de code « T-tournoi-match » se devine.
		if wantCode == "" || ModeIsole(r.GameMode) {
			r.SessionKey = nil
		}
		expurgerSessionProtegee(&r)
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

	if journal {
		fmt.Printf("[MM] criteres 0x34 pid=%d lu=%v bornes=%v filtre=%v : %s -> %d session(s), %d ecartee(s) par les criteres\n",
			conn.PID, critereLu, bornes, filtrer, texteCriteres([]MatchmakeSessionSearchCriteria{criteria}), len(sessions), filtrees)
	}
	out := NewStreamOut(s)
	out.U32(uint32(len(sessions)))
	for _, ss := range sessions {
		out.Add(ss)
	}
	// Filtre actif (pour tout joueur qui l'a, pas seulement les PID de test) : critere reel
	// resume et salons ecartes par champ, pour verifier sur les vrais joueurs ce que la liste
	// perd (une ligne par recherche, comme avant, bornee : texteCriteresCourts, texteRaisons).
	if filtrer {
		fmt.Printf("[MM] browseNoHolder -> %d public session(s) ; filtre pid=%d %s : %d ecartee(s) %s\n",
			len(sessions), conn.PID, texteCriteresCourts([]MatchmakeSessionSearchCriteria{criteria}), filtrees, texteRaisons(raisons))
	} else {
		fmt.Printf("[MM] browseNoHolder -> %d public session(s)\n", len(sessions))
	}
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
		// Lecture par identifiant ouverte a tous : jamais le mot de passe, et pas la cle
		// d'une session protegee (les deux sortaient telles quelles).
		expurgerSessionProtegee(&r)
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
	// Arene protegee : ses stations ne vont qu'a ses participants (le joueur admis par
	// 0x6D.0x27 l'est avant de les demander). Un curieux recoit la meme liste vide qu'un
	// identifiant inconnu au lieu de l'adresse de l'hote.
	// Meme regle pour une session isolee (ModeIsole, match de tournoi SSBU) : ses joueurs y
	// sont entres par AutoMatchmakeWithParam avant de demander les stations.
	ferme := g != nil && (sessionProtegee(g.session) || ModeIsole(g.session.GameMode)) && !containsPID(g.participants, conn.PID)
	m.mu.Unlock()
	if ferme {
		fmt.Printf("[MM] GetSessionURLs pid=%d gid=%d : session protegee ou isolee, demandeur hors session -> liste vide\n", conn.PID, gid)
		return sessionURLsResponse(conn, req, nil)
	}

	var host *Connection
	if g != nil {
		host = conn.Endpoint.FindConnectionByID(g.hostConnID)
	}

	if host == nil {
		return sessionURLsResponse(conn, req, nil)
	}

	// Hote pas pret (aucun ReplaceURL depuis son Register ou sa derniere participation) :
	// ses stations decrivent une session morte, et le pont les servirait quand meme (sa
	// garde RVCID ne joue pas, le Register pose deja le RVCID). Si son ReplaceURL est
	// attendu (natAttendu), meme traitement que bridgeNoRVCID : on l'attend hors de la
	// goroutine du visiteur. Sinon il n'y a rien a attendre : la reponse d'avant, tout de
	// suite, par le chemin d'origine ci-dessous. Un hote pret ne passe jamais ici. L'hote
	// qui demande ses propres URL n'attend pas.
	if host != conn && !host.natEstPret() {
		quoi, age := host.natRaz()
		attendu, raison := m.hoteAttendu(gid, host)
		switch {
		case attendu && conn.natPrendreAttenteURL():
			natDiagf("[NAT/diag] GetSessionURLs pid=%d : hote pid=%d pas pret (%s il y a %dms, aucun ReplaceURL depuis) -> attente de son ReplaceURL, %v au plus\n",
				conn.PID, host.PID, quoi, age.Milliseconds(), hostReplaceURLWait)
			go func() {
				defer conn.natRendreAttenteURL()
				defer natProtege("answerSessionURLsWhenHostIsPret")
				m.answerSessionURLsWhenHostIsPret(conn, req, host, gid)
			}()

			return nil
		case attendu:
			// Plafond natAttentesURLMax : cette connexion attend deja autant d'hotes. Aucune
			// goroutine de plus, la reponse d'avant tout de suite.
			natDiagf("[NAT/diag] GetSessionURLs pid=%d : hote pid=%d pas pret mais %d attentes deja en cours pour ce visiteur (plafond) -> reponse d'avant, sans attente\n",
				conn.PID, host.PID, natAttentesURLMax)
		default:
			natDiagf("[NAT/diag] GetSessionURLs pid=%d : hote pid=%d pas pret (%s il y a %dms) mais aucun ReplaceURL attendu (%s) -> reponse d'avant, sans attente\n",
				conn.PID, host.PID, quoi, age.Milliseconds(), raison)
		}
	}

	// Hand over the host's REAL UDP endpoint, not the WebSocket TCP port it registered:
	// the latter is unreachable for Pia's hole-punch, so the joiner's probe never lands
	// and the console stalls at MatchMakingExt m=1.
	urls, status := natBridgeStations(host.Stations(), m.PublicStationFirst)
	if status != bridgeNoRVCID {
		// Either it worked, or nothing a moment brings will change it.
		return sessionURLsResponse(conn, req, relayedFor(conn, host, urls))
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
	//
	// Meme plafond par connexion que l'attente du ReplaceURL ci-dessus (natpret.go) : une
	// goroutine par requete, sans borne, se laissait multiplier par un appelant qui boucle.
	// Au-dela, la reponse que l'attente donne a son echeance, tout de suite.
	if !conn.natPrendreAttenteURL() {
		fmt.Printf("[MM] GetSessionURLs pid=%d: %d attentes deja en cours (plafond) -> raw stations\n", conn.PID, natAttentesURLMax)

		return sessionURLsResponse(conn, req, relayedFor(conn, host, host.Stations()))
	}
	go func() {
		defer conn.natRendreAttenteURL()
		defer natProtege("answerSessionURLsWhenHostIsReady")
		m.answerSessionURLsWhenHostIsReady(conn, req, host)
	}()

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
	m.answerSessionURLsWhenHostIsReadyBy(conn, req, host, time.Now().Add(hostReplaceURLWait))
}

// answerSessionURLsWhenHostIsReadyBy est l'attente du RVCID ci-dessus, bornee par une
// echeance donnee : answerSessionURLsWhenHostIsPret y passe la sienne, pour que les deux
// attentes ne se cumulent plus (jusqu'a 5 s avant).
func (m *Matchmaking) answerSessionURLsWhenHostIsReadyBy(conn *Connection, req *RMCMessage, host *Connection, deadline time.Time) {
	for time.Now().Before(deadline) {
		time.Sleep(hostReplaceURLPoll)

		if urls, status := natBridgeStations(host.Stations(), m.PublicStationFirst); status == bridgeOK {
			fmt.Printf("[MM] GetSessionURLs pid=%d: host reported its ReplaceURL -> bridged\n", conn.PID)
			conn.SendRMC(sessionURLsResponse(conn, req, relayedFor(conn, host, urls)))

			return
		}
	}

	// Out of time: answer with whatever the host reported. Not answering at all would
	// leave the joiner hanging until ITS timeout, which is strictly worse.
	fmt.Printf("[MM] GetSessionURLs pid=%d: host never reported its ReplaceURL -> raw stations\n", conn.PID)
	conn.SendRMC(sessionURLsResponse(conn, req, relayedFor(conn, host, host.Stations())))
}

// answerSessionURLsWhenHostIsPret attend qu'un hote pas pret fasse son ReplaceURL, tant que
// celui-ci est attendu (natAttendu, re-evalue a chaque tour : un retour dans le salon
// relance le budget), puis repond exactement comme getSessionURLs l'aurait fait pour un hote
// pret. Passe le delai, ou des que le ReplaceURL n'est plus attendu, il repond ce qu'on
// repondait avant ce correctif : le visiteur n'y perd que l'attente. Une seule echeance pour
// tout, attente du RVCID comprise.
func (m *Matchmaking) answerSessionURLsWhenHostIsPret(conn *Connection, req *RMCMessage, host *Connection, gid uint32) {
	debut := time.Now()
	deadline := debut.Add(hostReplaceURLWait)
	for {
		if host.natEstPret() {
			natDiagf("[NAT/diag] GetSessionURLs pid=%d : hote pid=%d pret apres %dms -> stations de sa nouvelle session\n",
				conn.PID, host.PID, time.Since(debut).Milliseconds())

			break
		}
		if !time.Now().Before(deadline) {
			natDiagf("[NAT/diag] GetSessionURLs pid=%d : hote pid=%d toujours pas pret apres %v -> stations telles quelles (reponse d'avant le correctif)\n",
				conn.PID, host.PID, hostReplaceURLWait)

			break
		}
		if attendu, raison := m.hoteAttendu(gid, host); !attendu {
			natDiagf("[NAT/diag] GetSessionURLs pid=%d : hote pid=%d pas pret apres %dms, ReplaceURL plus attendu (%s) -> stations telles quelles (reponse d'avant le correctif)\n",
				conn.PID, host.PID, time.Since(debut).Milliseconds(), raison)

			break
		}
		time.Sleep(hostReplaceURLPoll)
	}

	urls, status := natBridgeStations(host.Stations(), m.PublicStationFirst)
	if status == bridgeNoRVCID {
		// Le chemin d'origine d'un hote auquel il manque le RVCID, borne par la MEME
		// echeance : une fois passee, il repond aussitot les stations telles quelles.
		m.answerSessionURLsWhenHostIsReadyBy(conn, req, host, deadline)

		return
	}
	conn.SendRMC(sessionURLsResponse(conn, req, relayedFor(conn, host, urls)))
}

// salonOuvert dit si le salon gid existe et accepte encore autoMatchmake (OpenParticipation,
// la condition d'autoMatchmake) : dans les journaux de prod, les 8 retours dans le meme
// salon moins de 3 s apres un EndParticipation passent tous par autoMatchmake.
func (m *Matchmaking) salonOuvert(gid uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gatherings[gid]

	return g != nil && g.session != nil && g.session.OpenParticipation
}

// hoteAttendu : natAttendu de l'hote pour un visiteur du salon gid, plus un cas que
// l'hote seul ne voit pas. S'il est sorti de gid (EndParticipation) puis entre dans un
// AUTRE salon, sa derniere remise a zero est une participation, mais son prochain
// ReplaceURL decrira la session de cet autre salon : rien a y gagner pour un visiteur de
// gid, qui recoit la reponse d'avant sans attendre (avant : jusqu'a 2,5 s d'attente pour
// les stations de la mauvaise session). L'hote entre dans gid par autoMatchmake,
// createSession ou joinSession y est participant avant sa remise a zero (participants mis
// a jour sous m.mu, natRAZ apres) : ce cas ne le touche pas.
func (m *Matchmaking) hoteAttendu(gid uint32, host *Connection) (bool, string) {
	m.mu.Lock()
	g := m.gatherings[gid]
	ouvert := g != nil && g.session != nil && g.session.OpenParticipation
	dedans := g != nil && containsPID(g.participants, host.PID)
	m.mu.Unlock()

	attendu, raison := host.natAttendu(ouvert)
	if attendu && !dedans && host.natDerniereRAZ() == natGenreParticipation {
		return false, "hote entre depuis dans un autre salon, son ReplaceURL decrira cette autre session"
	}

	return attendu, raison
}

// relayedFor repointe les stations vers le port de la paire (visiteur, hote) quand le
// relais par paire est arme. Applique au moment de REPONDRE, jamais dans le pont NAT :
// le pont ne connait que les stations, pas qui les demande, alors que le port depend des
// deux PID.
func relayedFor(joiner, host *Connection, urls []*StationURL) []*StationURL {
	if !PairRelayActive() || joiner == nil || host == nil || len(urls) == 0 {
		return urls
	}
	ipJ, ipH := PublicIPOf(joiner), PublicIPOf(host)
	rHost, rPort, ok := PairRelayFor(joiner.PID, host.PID, ipJ, ipH)
	if !ok {
		return urls
	}
	fmt.Printf("[relais-paire] URL de session pid=%d -> hote pid=%d : %s:%d\n", joiner.PID, host.PID, rHost, rPort)
	NoterSessionRelayee(joiner.PID, host.PID)

	return RelayStations(urls, rHost, rPort)
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
		// Salon orphelin (matchmaking_hote.go) : OwnerPID reste celui du proprietaire parti
		// jusqu'a UpdateSessionHost ; revenu, il n'est plus du salon et ne le supprime pas.
		mine = owner == conn.PID && (!g.orphelin || containsPID(g.participants, conn.PID))
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
	var elagues []uint64
	if g != nil && g.session != nil {
		g.session.OpenParticipation = open

		// [Nextendo] A LA FERMETURE, on retire les participants injoignables.
		//
		// Tant qu'on cherche, un joueur deconnecte garde sa place : sa console rouvre sa
		// connexion sans arret et le retirer aussitot vidait les salons (voir le delai de
		// grace cote jeu). Mais fermer la participation, c'est arreter de chercher et
		// commencer la partie — et a cet instant la liste cesse d'etre une file d'attente
		// pour devenir la COMPOSITION du match.
		//
		// Un absent qui y reste est bien pire qu'un salon a moitie vide : mesure du
		// 2026-09-02 sur SMM2, un salon annonce a quatre dont seules trois consoles
		// appelaient StartBattleMode. Les autres attendaient un joueur qui n'arriverait
		// jamais, la partie ne se terminait pas, et a l'ecran ca donnait « on arrive au but
		// et il ne se passe rien ».
		//
		// Celui qui ferme est toujours garde : c'est lui qui parle, il est donc la.
		if !open && conn.Endpoint != nil {
			vivants := make([]uint64, 0, len(g.participants))
			for _, pid := range g.participants {
				if pid == conn.PID || conn.Endpoint.FindConnectionByPID(pid) != nil {
					vivants = append(vivants, pid)

					continue
				}
				elagues = append(elagues, pid)
			}
			if len(elagues) > 0 {
				g.participants = vivants
				g.session.NumParticipants = uint32(len(vivants))
			}
		}
	}
	m.mu.Unlock()

	if len(elagues) > 0 {
		fmt.Printf("[MM] fermeture gid=%d : %d participant(s) injoignable(s) retire(s) %v -> match a %d\n",
			gid, len(elagues), elagues, len(g.participants))
	}

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
// 2306-0103 immediately after the results screen. This was implemented in the the previous stack stack
// and lost when Splatoon 2 moved to this core.
func (m *Matchmaking) updateSessionPart(conn *Connection, req *RMCMessage) *RMCMessage {
	// Smash : mise a jour appliquee selon les drapeaux du jeu (matchmaking_maj.go), et contenu
	// journalise pour les PID de test.
	smash := m.SessionPartSmash != nil && m.SessionPartSmash(conn)
	journal := m.JournalAppariement != nil && m.JournalAppariement(conn)
	if smash || journal {
		p, lu := lireMajSession(req.Body, conn.Settings)
		if journal {
			journalMajSession(conn, &p, lu)
		}
		if smash {
			fait := "illisible, acquitte"
			if lu {
				fait = m.appliquerMajSmash(conn, &p)
			}
			fmt.Printf("[MM] UpdateMatchmakeSessionPart pid=%d gid=%d drapeaux=%#x : %s\n", conn.PID, p.GID, p.ModificationFlags, fait)
			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
		}
	}
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

	// On collecte les avis a emettre SOUS le verrou, et on les envoie APRES l'avoir
	// relache : SendRMC ecrit sur une socket et peut bloquer, et tenir m.mu pendant ce
	// temps gelerait tout le matchmaking pour les autres joueurs.
	type avis struct {
		conn         *Connection
		gid          uint32
		proprietaire uint64
	}
	var aPrevenir, isoles []avis

	type migration struct {
		gid             uint32
		ancien, nouveau uint64
		participants    []uint64
	}
	var aMigrer []migration

	m.mu.Lock()
	for gid, g := range m.gatherings {
		if g == nil || g.session == nil {
			continue
		}
		before := len(g.participants)
		g.participants = removePID(g.participants, pid)
		if len(g.participants) == before {
			continue // not in this gathering
		}
		// Salon vide : il n'y a plus rien a garder.
		if len(g.participants) == 0 {
			delete(m.gatherings, gid)
			if g.code != "" {
				delete(m.byCode, g.code)
			}
			fmt.Printf("[MM] disconnect pid=%d -> gathering %d removed\n", pid, gid)
			continue
		}

		// LE PROPRIETAIRE PART, MAIS IL RESTE DU MONDE.
		//
		// On DETRUISAIT le salon, ce qui ejectait tous les autres avec lui. Symptome dans
		// SUPER MARIO BROS. 35 : l'hote s'en va et trois ou quatre consoles tombent dans
		// les 80 millisecondes qui suivent — pas chacune de son cote, mais parce qu'on leur
		// retirait la partie sous les pieds.
		//
		// Le vrai service MIGRE : il choisit un successeur parmi ceux qui restent et
		// previent tout le salon (type 4000). Le drapeau 0x10 dit si le titre l'autorise ;
		// SMB35 le pose (flags=0x210).
		if g.session.Gathering.OwnerPID == pid {
			// Migration d'hote par les consoles (matchmaking_hote.go) : le salon reste, le
			// successeur elu appellera UpdateSessionHost. Jamais pour une session isolee.
			if !m.NotifierDeparts && !ModeIsole(g.session.GameMode) && m.garderSalonSansProprietaire(g) {
				g.orphelin = true
				g.session.NumParticipants = uint32(len(g.participants))
				fmt.Printf("[MM] disconnect pid=%d -> proprietaire parti, salon %d garde pour la migration d'hote (%d restant(s))\n", pid, gid, len(g.participants))
				continue
			}
			if !m.NotifierDeparts || g.session.Gathering.Flags&0x10 == 0 {
				delete(m.gatherings, gid)
				if g.code != "" {
					delete(m.byCode, g.code)
				}
				fmt.Printf("[MM] disconnect pid=%d -> gathering %d removed (pas de migration)\n", pid, gid)
				continue
			}
			ancien := g.session.Gathering.OwnerPID
			nouveau := g.participants[0]
			g.session.Gathering.OwnerPID = nouveau
			g.session.Gathering.HostPID = nouveau
			if m.Endpoint != nil {
				if c := m.Endpoint.FindConnectionByID(g.hostConnID); c != nil {
					_ = c
				}
			}
			aMigrer = append(aMigrer, migration{gid: gid, ancien: ancien, nouveau: nouveau,
				participants: append([]uint64(nil), g.participants...)})
			fmt.Printf("[MM] proprietaire pid=%d parti -> salon %d migre vers pid=%d\n", pid, gid, nouveau)
		}
		g.session.NumParticipants = uint32(len(g.participants))
		fmt.Printf("[MM] disconnect pid=%d -> left gathering %d (%d left)\n", pid, gid, len(g.participants))

		// Session isolee (match de tournoi) : le proprietaire est toujours prevenu
		// (signalerDepartIsole), meme sans NotifierDeparts.
		if !m.NotifierDeparts && ModeIsole(g.session.GameMode) && m.Endpoint != nil {
			isoles = append(isoles, avis{gid: gid, proprietaire: g.session.Gathering.OwnerPID})
		}

		// Le proprietaire tient la liste des joueurs de la partie : s'il n'apprend pas les
		// departs, elle ne fait que grossir.
		if m.NotifierDeparts {
			if m.Endpoint != nil {
				if c := m.Endpoint.FindConnectionByPID(g.session.Gathering.OwnerPID); c != nil {
					aPrevenir = append(aPrevenir, avis{conn: c, gid: gid})
				}
			}
		}
	}
	m.mu.Unlock()

	// La migration se signale a TOUT le salon : chacun doit savoir qui commande.
	for _, mg := range aMigrer {
		for _, p := range mg.participants {
			if m.Endpoint == nil {
				break
			}
			c := m.Endpoint.FindConnectionByPID(p)
			if c == nil {
				continue
			}
			SendNotification(c, &NotificationEvent{
				PIDSource: mg.ancien,
				Type:      NotificationOwnershipChanged,
				Param1:    uint64(mg.gid),
				Param2:    mg.nouveau,
			})
		}
	}

	for _, a := range isoles {
		m.signalerDepartIsole(m.Endpoint, a.gid, pid, a.proprietaire, NotificationParticipantDisconnected)
	}

	for _, a := range aPrevenir {
		SendNotification(a.conn, &NotificationEvent{
			PIDSource: pid,
			Type:      NotificationParticipantDisconnected,
			Param1:    uint64(a.gid),
			Param2:    pid,
		})
		fmt.Printf("[MM] depart de pid=%d signale au proprietaire du salon %d\n", pid, a.gid)
	}
}

func (m *Matchmaking) endParticipation(conn *Connection, req *RMCMessage) {
	gid := NewStreamIn(req.Body, conn.Settings).U32()
	// La console quitte sa session Pia : ses stations sont celles d'une session morte
	// jusqu'a son prochain ReplaceURL. Cas du 15/10 22:46:36 : l'hote sort (0x32.1), deux
	// visiteurs recoivent son ancien port 60541 au lieu de 60493 (natpret.go).
	conn.natRAZ("EndParticipation")
	var proprietaire uint64
	hoteSorti, migrer := false, false
	m.mu.Lock()
	if g := m.gatherings[gid]; g != nil {
		hoteSorti = g.session.Gathering.OwnerPID == conn.PID || g.session.HostPID == conn.PID
		g.participants = removePID(g.participants, conn.PID)
		if len(g.participants) == 0 {
			delete(m.gatherings, gid)
		} else {
			g.session.NumParticipants = uint32(len(g.participants))
			if ModeIsole(g.session.GameMode) && g.session.Gathering.OwnerPID != conn.PID {
				proprietaire = g.session.Gathering.OwnerPID
			}
			// The owner leaves while others remain (SortieProprietaire, opt-in): a remaining
			// participant takes the gathering over if the game allows it (flag 0x10), else it goes.
			if hoteSorti && m.SortieProprietaire && !ModeIsole(g.session.GameMode) {
				if g.session.Gathering.Flags&0x10 != 0 {
					migrer = true
				} else {
					delete(m.gatherings, gid)
					if g.code != "" {
						delete(m.byCode, g.code)
					}
					fmt.Printf("[MM] gid=%d removed: owner %d left (EndParticipation), no takeover (flag 0x10 unset)\n", gid, conn.PID)
				}
			}
		}
	}
	m.mu.Unlock()
	if proprietaire != 0 && conn.Endpoint != nil {
		m.signalerDepartIsole(conn.Endpoint, gid, conn.PID, proprietaire, NotificationEndParticipation)
	}
	if migrer {
		m.migrerSalonApresSortie(gid, conn.PID, conn.Endpoint)
	}
}

// signalerDepartIsole previent le proprietaire d'une session isolee (match de tournoi SSBU,
// ModeIsole) qu'un participant en est parti (3008 EndParticipation, 3007 deconnexion),
// comme le service d'origine. Sans cet avis, l'hote garde le partant dans la composition
// du match : cas du 16/10 14:32-14:34, le visiteur rate deux percages, sort deux fois par
// 0x32.1, abandonne le tournoi ; l'hote, jamais prevenu, reste 105 s en recherche puis fige
// apres sa fermeture de participation (0x6d.1 repete, ni m13 ni m14). Dans le cas voisin
// (14:37-14:39) ou personne n'etait jamais entre dans le salon, l'hote est sorti seul en 4 s
// (m13). Meme correctif que NotifierDeparts, limite aux sessions isolees.
func (m *Matchmaking) signalerDepartIsole(ep *Endpoint, gid uint32, partant, proprietaire uint64, genre uint32) {
	c := ep.FindConnectionByPID(proprietaire)
	if c == nil {
		return
	}
	SendNotification(c, &NotificationEvent{PIDSource: partant, Type: genre, Param1: uint64(gid), Param2: partant})
	fmt.Printf("[MM] session isolee gid=%d : depart de pid=%d signale au proprietaire pid=%d (type %d)\n", gid, partant, proprietaire, genre)
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

// gatheringDuPID (sous m.mu) : LA session de pid quand il en a plusieurs (jusqu'a
// sessionsSeulMax ou il est seul, plus celles ou il est entre). Choix deterministe, au lieu du
// premier rencontre dans l'ordre aleatoire de la map : d'abord une session ou il n'est pas
// seul (la vraie partie), puis la plus recente (gid le plus haut).
func (m *Matchmaking) gatheringDuPID(pid uint64) *gathering {
	var choisie *gathering
	for _, g := range m.gatherings {
		if g == nil || g.session == nil || !containsPID(g.participants, pid) {
			continue
		}
		if choisie == nil {
			choisie = g
			continue
		}
		accompagnee, accompagneeChoisie := len(g.participants) > 1, len(choisie.participants) > 1
		if accompagnee != accompagneeChoisie {
			if accompagnee {
				choisie = g
			}
			continue
		}
		if g.session.ID > choisie.session.ID {
			choisie = g
		}
	}
	return choisie
}

// GatheringIDByPID returns the id of the gathering the given PID is in (gatheringDuPID
// when it is in several). Titles that publish "which room is my friend in" need the id
// itself, not just the participant list. Must NOT be called while holding m.mu.
func (m *Matchmaking) GatheringIDByPID(pid uint64) (uint32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.gatheringDuPID(pid); g != nil {
		return g.session.ID, true
	}
	return 0, false
}

// SessionByPID returns the participant PID list of the gathering the given PID is in
// (gatheringDuPID when it is in several), or nil when it is in none. Must NOT be called
// while holding m.mu.
func (m *Matchmaking) SessionByPID(pid uint64) []uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.gatheringDuPID(pid); g != nil {
		out := make([]uint64, len(g.participants))
		copy(out, g.participants)
		return out
	}
	return nil
}

// isTournamentIDList reconnaît la requête « donne-moi ces tournois » de Mario
// Kart : u32 nombre, suivi d'exactement autant d'identifiants sur 32 bits (la
// measured en montre 85, soit 344 octets). La sonde SSBU qui partage ce numéro
// de méthode n'a pas cette forme, d'où un tri fiable sans connaître le jeu.
func isTournamentIDList(body []byte) bool {
	if len(body) < 8 || len(body)%4 != 0 {
		return false
	}
	n := uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24
	return n > 0 && n <= 4096 && int(n)*4+4 == len(body)
}

// ParticipantDetails : une entree de la liste rendue par GetDetailedParticipants
// (MatchMaking 0x15, methode 15). Structure documentee par kinnay :
// PID + nom + message + nombre de participants.
type ParticipantDetails struct {
	IDParticipant uint64
	Name          string
	Message       string
	Participants  uint16
}

// Levels implements Structure.
func (p *ParticipantDetails) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.PID(p.IDParticipant)
			o.String(p.Name)
			o.String(p.Message)
			o.U16(p.Participants)
		},
		Load: func(i *StreamIn) {
			p.IDParticipant = i.PID()
			p.Name = i.String()
			p.Message = i.String()
			p.Participants = i.U16()
		},
	}}
}

// getDetailedParticipants repond a MatchMaking 0x0F. PAC-MAN 99 l appelle JUSTE APRES
// AutoMatchmake : il vient d entrer dans la salle et demande qui s y trouve. Sans reponse
// il s arrete sur 2306-0103, salle creee mais partie jamais lancee.
//
// Le nom et le message restent vides : le serveur ne connait ici que des PID, et le jeu
// affiche de toute facon le pseudo que chaque console porte elle-meme. Si un client se
// revele exigeant sur ces champs, c est la premiere chose a regarder.
func (m *Matchmaking) getDetailedParticipants(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	gid := NewStreamIn(req.Body, s).U32()

	m.mu.Lock()
	g := m.gatherings[gid]
	var pids []uint64
	if g != nil {
		pids = append(pids, g.participants...)
	}
	m.mu.Unlock()

	out := NewStreamOut(s)
	out.U32(uint32(len(pids)))
	for _, pid := range pids {
		out.Add(&ParticipantDetails{
			IDParticipant: pid,
			Participants:  1,
		})
	}
	fmt.Printf("[MM] getDetailedParticipants gid=%d -> %d participant(s)\n", gid, len(pids))

	return NewRMCSuccess(s, ProtocolMatchMaking, req.Method, req.CallID, out.Bytes())
}
