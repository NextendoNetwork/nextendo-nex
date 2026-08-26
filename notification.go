package nex

import (
	"crypto/rand"
	"os"
	"sync/atomic"
)

// Notifications protocol (server → client push only).
const (
	ProtocolNotifications          uint16 = 0x0E
	MethodProcessNotificationEvent uint32 = 0x1
)

// Notification event type ids (category*1000 + subtype). These are what the
// console's Pia WaitNotification blocks on after matchmaking.
const (
	NotificationParticipate           uint32 = 3001   // ParticipationEvent.Participate — the pairing unblock
	NotificationEndParticipation      uint32 = 3008   // ParticipationEvent.EndParticipation
	NotificationAddedToGathering      uint32 = 122000 // to a newly-added (non-caller) participant
	NotificationGatheringUnregistered uint32 = 109000 // gathering torn down
	NotificationHostChanged           uint32 = 110000 // host migrated
)

// NotificationEvent is the payload of ProcessNotificationEvent (NEX 4.0). It is
// StructureVersion 0 with all-u64 params and NO MapParam — the MapParam / u32
// variants are rejected outright by the Switch.
type NotificationEvent struct {
	PIDSource uint64
	Type      uint32
	Param1    uint64
	Param2    uint64
	StrParam  string
	Param3    uint64
}

// Levels implements Structure.
func (n *NotificationEvent) Levels() []Level {
	return []Level{{
		Version: 0,
		Save: func(o *StreamOut) {
			o.PID(n.PIDSource)
			o.U32(n.Type)
			o.U64(n.Param1)
			o.U64(n.Param2)
			// La chaine VIDE : longueur 1 + terminateur (String) ou longueur 0
			// (StringVideZero) ? MK8 et les autres acceptent la premiere forme, mais ils ne
			// dependent d'aucun envoi du serveur — PAC-MAN 99 est le premier titre ou nous
			// POUSSONS, et il ignore tout ce qu'on lui envoie. Un octet de trop ici decale
			// tout ce qui suit et l'evenement devient illisible, en silence.
			// Pilote par NEX_NOTIF_STRVIDE0=1 pour trancher sans casser les autres jeux.
			if notifStrVideZero {
				o.StringVideZero(n.StrParam)
			} else {
				o.String(n.StrParam)
			}
			o.U64(n.Param3)
		},
		Load: func(i *StreamIn) {
			n.PIDSource = i.PID()
			n.Type = i.U32()
			n.Param1 = i.U64()
			n.Param2 = i.U64()
			n.StrParam = i.String()
			n.Param3 = i.U64()
		},
	}}
}

// notifStrVideZero : voir le commentaire dans Levels().
var notifStrVideZero = os.Getenv("NEX_NOTIF_STRVIDE0") == "1"

var notifCallCounter uint32

// SendNotification pushes a ProcessNotificationEvent RMC *request* to the target
// connection. The addressing (source stream id) is inherited from the target
// connection's own response path — which is exactly what the Switch requires
// (a hardcoded source port is silently dropped).
func SendNotification(target *Connection, event *NotificationEvent) {
	s := target.Settings
	out := NewStreamOut(s)
	out.Add(event)
	callID := 0xFFFE0000 + atomic.AddUint32(&notifCallCounter, 1)
	target.SendRMC(NewRMCRequest(s, ProtocolNotifications, MethodProcessNotificationEvent, callID, out.Bytes()))
}

// randomBytes returns n cryptographically-random bytes (session keys etc.).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
