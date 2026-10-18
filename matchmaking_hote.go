package nex

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Migration d'hote : MatchMaking UpdateSessionHost (0x15.0x2A).
//
// CE QU'ATTEND LE SUCCESSEUR (Smash 13.0.5, Pia ; prouve en executant le code du jeu,
// un outil interne). Quand l'hote quitte le maillage, les
// consoles elisent un successeur. Celui-ci, dans NexPrepareForBecomingHost (0x110080), appelle
// UpdateSessionHost(gid, true) (0x1100dc-0x1100e4 ; en etat 3, session jointe, une seconde fois
// pour l'autre gid par NexMatchmakeSession vtable+0xd8 = 0xeb5a0), pose une echeance de 10 s
// (0x110148 : 0x2710 ms) et passe a WaitAfterPrepareForBecomingHost (0x1101a0). Cette attente
// ne lit ni la reponse RMC ni l'avis 110000 : une fois l'appel termine, elle interroge
// vtable+0xe0 (0x13d040 : octet +0x10) de chaque NexMatchmakeSession du gestionnaire
// [0x52adb38]+0x168, et passe a SendGreetingMessage (0x1a1a40) seulement quand il vaut 1 ;
// a l'echeance, HostMigrationFailure (0x1a1d80) et la console quitte le salon (0x32.0x1).
//
// Cet octet n'est ecrit que par l'avis 4000 (changement de proprietaire) : le gestionnaire
// Pia NexMatchMeshLayerController::NexNotificationEventHandler (0xef5d0, categories 3, 4, 109,
// 110, 120-122) prend, pour tout type 4xxx dont Param1 est le gid d'une session (0xefa98),
// vtable+0xe8(Param2 == PID local) puis vtable+0xf8(Param2) (0x13d050 / 0x13d030 : +0x10 et
// +8). L'avis 110000 n'y fait rien (table 0x4495fa8, categorie 110 -> sortie) ; il ne sert qu'au
// drapeau +0x1f9 du job (0x112630), lu seulement par 0x1122b0 et seulement sans gestionnaire
// de sessions ([0x52adb38] nul), ce qui n'est pas le cas de Smash en ligne.
//
// Les 4xxx passent avant cela par le filtre du NotificationEventManager (0x22de80) : texte
// vide, accepte ; sinon le texte est lu par strtoull (0x1b5b00) et un avis dont le nombre n'est
// pas STRICTEMENT superieur au dernier recu pour le meme Param1 est jete (6 gid gardes). D'ou
// horodatage4000 : jamais deux fois la meme valeur, croissante dans l'ordre des changements.
//
// CE QUE FAIT NINTENDO (capture MK8 un outil interne, meme NEX 4 et meme Pia ;
// gid 0x8b32dd). L'appelant : reponse 0x15.0x2A vide (23:33:33.103), puis 4000 {pid =
// appelant, param1 = gid, param2 = appelant, texte = heure Unix en microsecondes
// '1786570413042327', param3 = 0} (23:33:33.184), et aucun 110000. Les autres participants
// (23:36:44, appel d'un autre joueur alors que l'ancien hote est encore dans le salon) : 4000
// puis 110000 {pid = nouvel hote, param1 = gid, param2 = 0, texte vide}, 1,5 ms d'ecart.
//
// CE QUI SE PASSAIT CHEZ NOUS. Seul 110000 partait (a tous, appelant compris). Le successeur ne
// voyait un 4000 a son nom que si la reprise du serveur (SortieProprietaire, depart du
// proprietaire par 0x32.0x1) l'avait choisi juste avant. Prod du 17/10 20:27Z au 18/10 01:24Z,
// 341 appels : 4000 a son nom deja recu, 3 departs a 10 s sur 143 ; 4000 pour un autre
// participant, 28 sur 28 ; aucun 4000, 157 sur 170 (mediane 10,0 s).
//
// CE QU'ON FAIT. Pour un appelant participant du salon : il en devient l'hote (HostPID, et la
// connexion qui sert les URL de session), et le proprietaire si isMigrateOwner. La reponse
// part d'abord, puis, si isMigrateOwner, le 4000 de Nintendo a chaque participant joignable
// (appelant compris), puis 110000 aux autres. Un appelant hors du salon est acquitte sans
// effet, comme une session isolee (match de tournoi, ModeIsole) : le moteur du tournoi suit
// son proprietaire, comme pour 0x6D.0x2C et la garde des salons orphelins.
//
// A TOUT MOMENT, PAS SEULEMENT APRES LE DEPART DU PROPRIETAIRE. C'est voulu, et c'est ce que
// fait Nintendo (23:36:44 : l'ancien hote recoit 4000 et 110000 avant son propre 0x32.0x1) :
// les consoles excluent l'ancien hote du maillage avant que le serveur ne le sache (prod du
// 17/10 : il n'en sort que 0,04 a 141 s apres l'appel).
func (m *Matchmaking) updateSessionHost(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	ok := NewRMCSuccess(s, ProtocolMatchMaking, req.Method, req.CallID, nil)
	if m.MigrationHote == nil || !m.MigrationHote(conn.PID) {
		return ok
	}
	in := NewStreamIn(req.Body, s)
	gid := in.U32()
	proprietaire := in.Bool()
	if in.Err() != nil {
		fmt.Printf("[MM] UpdateSessionHost pid=%d : corps illisible (%d octets) -> acquitte\n", conn.PID, len(req.Body))
		return ok
	}

	m.mu.Lock()
	g := m.gatherings[gid]
	membre := g != nil && g.session != nil && containsPID(g.participants, conn.PID)
	isole := membre && ModeIsole(g.session.GameMode)
	membre = membre && !isole
	var ancienHote, ancienProprio, nouveauProprio uint64
	var parts []uint64
	var texte string
	if membre {
		ancienHote, ancienProprio = g.session.HostPID, g.session.OwnerPID
		g.session.HostPID = conn.PID
		g.hostConnID = conn.ID
		g.orphelin = false
		if proprietaire {
			g.session.OwnerPID = conn.PID
			// participants[0] = proprietaire (updateApplicationBuffer, migrateGatheringOwnership).
			g.participants = append([]uint64{conn.PID}, removePID(g.participants, conn.PID)...)
			// Sous m.mu : l'ordre des horodatages est celui des changements de proprietaire.
			texte = horodatage4000()
		}
		nouveauProprio = g.session.OwnerPID
		parts = append([]uint64(nil), g.participants...)
	}
	m.mu.Unlock()

	if isole {
		fmt.Printf("[MM] UpdateSessionHost pid=%d gid=%d proprietaire=%v : session isolee (tournoi) -> acquitte sans effet\n", conn.PID, gid, proprietaire)
		return ok
	}
	if !membre {
		fmt.Printf("[MM] UpdateSessionHost pid=%d gid=%d proprietaire=%v : salon inconnu ou pid absent -> acquitte sans effet\n", conn.PID, gid, proprietaire)
		return ok
	}

	// La reponse d'abord, comme Nintendo (le successeur attend de toute facon la fin de
	// l'appel avant de lire ses sessions, 0x1101b4-0x1101dc).
	conn.SendRMC(ok)

	ep := conn.Endpoint
	cibles := make([]*Connection, 0, len(parts))
	for _, p := range parts {
		var c *Connection
		if p == conn.PID {
			c = conn
		} else if ep != nil {
			c = ep.FindConnectionByPID(p)
		}
		if c != nil {
			cibles = append(cibles, c)
		}
	}
	n4000, n110000 := 0, 0
	if proprietaire {
		for _, c := range cibles {
			SendNotification(c, &NotificationEvent{PIDSource: conn.PID, Type: NotificationOwnershipChanged,
				Param1: uint64(gid), Param2: conn.PID, StrParam: texte})
			n4000++
		}
	}
	for _, c := range cibles {
		if c == conn {
			continue
		}
		SendNotification(c, &NotificationEvent{PIDSource: conn.PID, Type: NotificationHostChanged, Param1: uint64(gid)})
		n110000++
	}
	fmt.Printf("[MM] UpdateSessionHost pid=%d gid=%d proprietaire=%v : hote %d -> %d, proprietaire %d -> %d, 4000 (%s) envoye a %d participant(s), 110000 a %d %v\n",
		conn.PID, gid, proprietaire, ancienHote, conn.PID, ancienProprio, nouveauProprio, texte, n4000, n110000, parts)

	return nil
}

// horodatage4000 rend le texte d'un avis 4000 : l'heure Unix en microsecondes en decimal
// (16 chiffres, comme Nintendo), strictement superieure a la valeur precedente du processus
// (le filtre 0x22de80 jette un 4000 dont le nombre ne depasse pas le dernier recu pour le meme
// gid).
func horodatage4000() string {
	horodatage4000Mu.Lock()
	defer horodatage4000Mu.Unlock()
	v := time.Now().UnixMicro()
	if v <= horodatage4000Dernier {
		v = horodatage4000Dernier + 1
	}
	horodatage4000Dernier = v

	return strconv.FormatInt(v, 10)
}

var (
	horodatage4000Mu      sync.Mutex
	horodatage4000Dernier int64
)

// garderSalonSansProprietaire (sous m.mu) : le proprietaire d'un salon s'est deconnecte et il
// reste des participants ; vrai si l'un d'eux a la migration d'hote (MigrationHote). Le salon
// reste alors en place, proprietaire inchange, jusqu'a ce que le successeur elu par les
// consoles appelle UpdateSessionHost ; sans successeur, il disparait avec son dernier
// participant (RemovePlayer, EndParticipation). D'ici la il est orphelin : ni appariement, ni
// liste publique, ni arenes d'amis (ses URL de session n'existent pas).
func (m *Matchmaking) garderSalonSansProprietaire(g *gathering) bool {
	if m.MigrationHote == nil || g == nil {
		return false
	}
	for _, p := range g.participants {
		if m.MigrationHote(p) {
			return true
		}
	}
	return false
}
