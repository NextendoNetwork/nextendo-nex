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

// friendNotificationsFor collecte les données publiées par les AMIS de l'appelant pour les
// types demandés. Sans source de liste d'amis configurée, la réponse est vide — c'est-à-dire
// le comportement qu'avait le cœur avant ce fichier, donc rien ne change pour les autres jeux.
func (m *Matchmaking) friendNotificationsFor(pid uint64, types []uint32) []*NotificationEvent {
	if m.FriendPIDs == nil {
		return nil
	}
	var out []*NotificationEvent
	for _, friend := range m.FriendPIDs(pid) {
		for _, typ := range types {
			ev := m.notif.get(friend, typ)
			if ev == nil {
				continue
			}
			// Filtre « porte ouverte ». Mesuré sur le stack de référence : dans la donnée de
			// notification ACNH, Param2 vaut 1 quand l'aéroport est OUVERT, 0 quand il est
			// fermé (l'hôte republie une donnée Param2=0 en fermant sa porte sans se
			// déconnecter). Un hôte fermé ne doit pas figurer au menu de visite.
			if ev.Param2 == 0 {
				continue
			}
			out = append(out, ev)
		}
	}
	return out
}

// getFriendNotificationData répond aux méthodes 10 (un type) et 13 (plusieurs types).
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

	out := NewStreamOut(s)
	out.U32(uint32(len(events)))
	for _, ev := range events {
		out.Add(ev)
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
