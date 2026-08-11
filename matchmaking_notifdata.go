package nex

// Données de notification entre amis (MatchmakeExtension 0x6D, méthodes 9 / 10 / 13).
//
// C'est le mécanisme par lequel Animal Crossing construit la liste des amis JOIGNABLES :
//   - l'hôte qui ouvre son aéroport publie une donnée de notification (méthode 9) ;
//   - le visiteur demande celles de ses amis (méthodes 10 et 13) ;
//   - chaque ami qui a publié apparaît dans la liste que propose Rounard.
//
// Sans ces méthodes, la liste revient vide et le jeu répond « il n'y a aucune île où nous
// puissions vous emmener » — même quand des amis ont bel et bien ouvert leur porte. C'est
// distinct de la recherche par participant (0x33), qui localise une île une fois l'ami CHOISI :
// l'une remplit le menu, l'autre y donne accès. Il faut les deux.
//
// Formats fil repris de NintendoClients (MIT), matchmaking.py :
//   9  UpdateNotificationData(u32 type, pid param1, pid param2, string param3) -> void
//   10 GetFriendNotificationData(s32 type)          -> List<NotificationEvent>
//   13 GetFriendNotificationDataList(List<u32> type) -> List<NotificationEvent>

import (
	"fmt"
	"sync"
)

const (
	// MethodUpdateNotificationData : l'hôte publie sa donnée (« ma porte est ouverte »).
	MethodUpdateNotificationData uint32 = 9
	// MethodGetFriendNotificationData : données des amis pour UN type.
	MethodGetFriendNotificationData uint32 = 10
	// MethodGetFriendNotificationDataList : données des amis pour PLUSIEURS types.
	MethodGetFriendNotificationDataList uint32 = 13
	// MethodGetFriendNotificationDataByPID : détails des amis signalés par les événements
	// de classe 128 (le client appelle cette méthode avec les PID des événements 128xxx).
	MethodGetFriendNotificationDataByPID uint32 = 15
)

// Classes d'événements reconnues par le client LM3 (vérifiées dans le binaire, m13
// handler sub_7100EBF9C0) : un événement n'est retenu que si type/1000 vaut 111
// (PID alimente directement la liste d'amis) ou 128 (le client repart chercher des
// détails via la méthode 15). Tout autre type — 101..108, 4000..4999 — est ignoré.
const (
	EventClassFriendPlaying uint32 = 111000 // type/1000 == 111
	EventClassFriendDetail  uint32 = 128000 // type/1000 == 128
)

// notifStore conserve, par joueur et par type, la dernière donnée de notification publiée.
// Volontairement en mémoire, comme le reste du matchmaking : une donnée survit à la session
// qui l'a produite mais pas au serveur, et une déconnexion la purge (voir RemovePlayer).
type notifStore struct {
	mu   sync.Mutex
	data map[uint64]map[uint32]*NotificationEvent
}

func newNotifStore() *notifStore {
	return &notifStore{data: map[uint64]map[uint32]*NotificationEvent{}}
}

func (n *notifStore) put(pid uint64, typ uint32, ev *NotificationEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.data[pid] == nil {
		n.data[pid] = map[uint32]*NotificationEvent{}
	}
	n.data[pid][typ] = ev
}

// get renvoie une COPIE de la donnée publiée par pid pour ce type, ou nil.
func (n *notifStore) get(pid uint64, typ uint32) *NotificationEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	if byType := n.data[pid]; byType != nil {
		if ev := byType[typ]; ev != nil {
			c := *ev
			return &c
		}
	}
	return nil
}

func (n *notifStore) forget(pid uint64) {
	n.mu.Lock()
	delete(n.data, pid)
	n.mu.Unlock()
}

// updateNotificationData enregistre la donnée publiée par l'appelant (méthode 9).
func (m *Matchmaking) updateNotificationData(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	typ := in.U32()
	p1 := in.PID()
	p2 := in.PID()
	str := in.String()
	if in.Err() != nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}

	ev := &NotificationEvent{
		PIDSource: conn.PID,
		Type:      typ,
		Param1:    p1,
		Param2:    p2,
		StrParam:  str,
	}
	m.notif.put(conn.PID, typ, ev)
	fmt.Printf("[MM] notification publiée pid=%d type=%d param1=%d param2=%d\n", conn.PID, typ, p1, p2)
	// POUSSE en temps réel aux amis EN LIGNE, comme le serveur the previous stack de référence : il n'attend
	// pas le polling (méthode 13), il envoie un ProcessNotificationEvent dès qu'un ami publie sa
	// présence. Mesuré sur measured : le type poussé = type_publié × 1000 (101 -> 101000, 109 ->
	// 109000). Sans ça la Pia d'ACNH n'a jamais l'événement de présence temps réel de l'hôte que
	// le visiteur attend pour finaliser la visite.
	m.pushNotifDataToFriends(conn, ev)
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// pushNotifDataToFriends envoie l'événement de présence de l'appelant à chacun de ses amis EN
// LIGNE (ProcessNotificationEvent). Type poussé = type × 1000 (forme mesurée sur the previous stack). Sans
// source d'amis (autres jeux), ne fait rien.
func (m *Matchmaking) pushNotifDataToFriends(conn *Connection, ev *NotificationEvent) {
	if m.FriendPIDs == nil {
		return
	}
	pushed := 0
	for _, friend := range m.FriendPIDs(conn.PID) {
		target := conn.Endpoint.FindConnectionByPID(friend)
		if target == nil {
			continue
		}
		SendNotification(target, &NotificationEvent{
			PIDSource: ev.PIDSource, Type: ev.Type * 1000,
			Param1: ev.Param1, Param2: ev.Param2, StrParam: ev.StrParam,
		})
		pushed++
	}
	if pushed > 0 {
		fmt.Printf("[MM] présence pid=%d POUSSÉE à %d ami(s) en ligne (type=%d)\n", conn.PID, pushed, ev.Type*1000)
	}
}

// PublishNotification enregistre une donnée de notification sous (pid, type) : c'est ce
// que m10/m13 renvoie aux AMIS de pid. Exporté pour que le jeu construise ses propres
// événements (layout « salle ouverte ») quand il publie pour son compte.
func (m *Matchmaking) PublishNotification(pid uint64, typ uint32, ev *NotificationEvent) {
	if ev == nil {
		return
	}
	m.notif.put(pid, typ, ev)
}

// friendNotificationsFor collecte les données publiées par les AMIS de l'appelant pour les
// types demandés. Sans source de liste d'amis configurée, la réponse est vide — c'est-à-dire
// le comportement qu'avait le cœur avant ce fichier, donc rien ne change pour les autres jeux.
func (m *Matchmaking) friendNotificationsFor(pid uint64, types []uint32) []*NotificationEvent {
	if m.FriendPIDs == nil {
		return nil
	}
	var out []*NotificationEvent
	// Un même (ami, type) ne doit pas revenir en double quand plusieurs kinds de la
	// requête 101..108 pointent vers le même événement.
	seen := map[uint64]map[uint32]bool{}
	add := func(friend uint64, ev *NotificationEvent) {
		if ev == nil || ev.Param2 == 0 {
			return
		}
		if seen[friend] == nil {
			seen[friend] = map[uint32]bool{}
		}
		if seen[friend][ev.Type] {
			return
		}
		seen[friend][ev.Type] = true
		out = append(out, ev)
	}
	for _, friend := range m.FriendPIDs(pid) {
		for _, typ := range types {
			if ev := m.notif.get(friend, typ); ev != nil {
				add(friend, ev)
				continue
			}
			if typ >= 101 && typ <= 108 {
				// LM3 : le client interroge les kinds 101..108, mais son gestionnaire
				// m13 (sub_7100EBF9C0) ne retient que les événements dont type/1000
				// vaut 111 ou 128 — 111 alimente directement la liste d'amis, 128
				// déclenche un aller-retour méthode 15. Les deux classes sont publiées
				// sous LM3_FRIEND_EVENT_TYPE ; on les sert toutes les deux ici.
				add(friend, m.notif.get(friend, EventClassFriendPlaying))
				add(friend, m.notif.get(friend, EventClassFriendDetail))
			}
		}
	}
	return out
}
func (m *Matchmaking) getFriendNotificationData(conn *Connection, req *RMCMessage, list bool) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)

	var types []uint32
	if list {
		types = ReadList(in, func(i *StreamIn) uint32 { return i.U32() })
	} else {
		types = []uint32{uint32(in.S32())}
	}
	if in.Err() != nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}

	events := m.friendNotificationsFor(conn.PID, types)

	// Layout des événements m13, piné dans le binaire (sub_7100E95B40) : chaque
	// élément porte un en-tête [u8 version][u32 longueur] puis ses champs, et le
	// string est préfixé par u16 (sub_7100E90F40). Le lecteur saute à la fin de
	// l'élément via version+len. On sérialise donc À LA MAIN, sans passer par le
	// writer de structures (dont le préfixe de string u32 désalignerait la trame) :
	//   [u8 0][u32 len=38+strlen][pid u64][type u32][p1 u64][p2 u64][str u16][p3 u64]
	out := NewStreamOut(s)
	out.U32(uint32(len(events)))
	for _, ev := range events {
		contentLen := 8 + 4 + 8 + 8 + 2 + len(ev.StrParam) + 8
		out.U8(0)
		out.U32(uint32(contentLen))
		out.U64(ev.PIDSource)
		out.U32(ev.Type)
		out.U64(ev.Param1)
		out.U64(ev.Param2)
		out.U16(uint16(len(ev.StrParam)))
		out.Write([]byte(ev.StrParam))
		out.U64(ev.Param3)
	}
	fmt.Printf("[MM] notifications d'amis pid=%d types=%v -> %d entrée(s)\n", conn.PID, types, len(events))
	resp := NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
	// En plus de la réponse au polling, POUSSE chaque présence d'ami au demandeur (comme the previous stack,
	// qui envoie un ProcessNotificationEvent type=×1000 par ami en ligne). Envoyé après la réponse
	// pour ne pas s'intercaler dedans.
	for _, ev := range events {
		SendNotification(conn, &NotificationEvent{
			PIDSource: ev.PIDSource, Type: ev.Type * 1000,
			Param1: ev.Param1, Param2: ev.Param2, StrParam: ev.StrParam,
		})
	}
	return resp
}

// getFriendNotificationDataByPID répond à la méthode 15 : le client l'appelle avec les PID
// des amis signalés par les événements de classe 128. Le parseur client (sub_7100E95E00)
// lit [u32 count], puis par objet : une chaîne u16-préfixée et un « Any » (u8 tag + valeur,
// sub_7100E91210). On renvoie { nom affichable, tag 1 + PID } par ami demandé.
func (m *Matchmaking) getFriendNotificationDataByPID(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	pids := ReadList(in, func(i *StreamIn) uint64 { return i.PID() })
	_ = in.Buffer()
	if in.Err() != nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}

	out := NewStreamOut(s)
	out.U32(uint32(len(pids)))
	for _, p := range pids {
		name := ""
		if m.FriendName != nil {
			name = m.FriendName(p)
		}
		out.U16(uint16(len(name)))
		out.Write([]byte(name))
		out.U8(1) // Any tag 1 = entier 64 bits
		out.U64(p)
	}
	fmt.Printf("[MM] notification data pid=%d amis=%v -> %d entrée(s)\n", conn.PID, pids, len(pids))
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}
