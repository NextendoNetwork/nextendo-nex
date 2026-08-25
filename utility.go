package nex

import "fmt"

// Utility protocol. MK8 calls GetIntegerSettings right after registering; we
// return empty setting maps, which is enough to unblock the connection sequence.
const (
	ProtocolUtility          uint16 = 0x6E
	MethodGetIntegerSettings uint32 = 0x7
	MethodGetStringSettings  uint32 = 0x8

	// A title asks for a NEX unique id the first time an account goes online, then keeps
	// it locally — so these are only ever called on a FRESH account, which is why an
	// unanswered one looks like "it works for most people". Splatoon 2 calls method 2 and
	// shows 2306-0103 if it is not answered. Both were implemented in the the previous stack stack and
	// lost when the games moved to this core.
	MethodAcquireNexUniqueID             uint32 = 0x1
	MethodAcquireNexUniqueIDWithPassword uint32 = 0x2
)

// nexUniqueIDPasswordSalt derives a unique-id password from the id itself.
//
// The password only has to be stable and to match what we hand out: nothing verifies it
// against a store, because the id IS the account's PID and the account is already
// authenticated by its Kerberos ticket before any of this runs. Same constant as the
// the previous stack stack, so an account that acquired its id there keeps working here.
const nexUniqueIDPasswordSalt uint64 = 0x4e45585f50574421

// UniqueIDInfo is the Utility structure returned by AcquireNexUniqueIDWithPassword:
// the id plus the password the title stores alongside it.
type UniqueIDInfo struct {
	NEXUniqueID         uint64
	NEXUniqueIDPassword uint64
}

// Levels implements Structure.
func (u *UniqueIDInfo) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.U64(u.NEXUniqueID); o.U64(u.NEXUniqueIDPassword) },
		Load: func(i *StreamIn) { u.NEXUniqueID = i.U64(); u.NEXUniqueIDPassword = i.U64() },
	}}
}

// UtilityHandler answers the settings queries with empty maps, and hands out a NEX unique
// id derived from the caller's PID.
func UtilityHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodGetIntegerSettings, MethodGetStringSettings:
			out := NewStreamOut(conn.Settings)
			out.U32(0) // empty Map<u16, ...>
			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		case MethodAcquireNexUniqueID:
			// Derived from the PID rather than allocated: it must survive restarts and stay
			// the same id across sessions, and the PID already is a stable per-account
			// number we own. Allocating a counter would need storage and would hand a
			// returning account a different id than the one it kept.
			out := NewStreamOut(conn.Settings)
			out.U64(uint64(conn.PID))
			fmt.Printf("[Utility] AcquireNexUniqueID pid=%d -> %d\n", conn.PID, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		case MethodAcquireNexUniqueIDWithPassword:
			out := NewStreamOut(conn.Settings)
			out.Add(&UniqueIDInfo{
				NEXUniqueID:         uint64(conn.PID),
				NEXUniqueIDPassword: uint64(conn.PID) ^ nexUniqueIDPasswordSalt,
			})
			fmt.Printf("[Utility] AcquireNexUniqueIDWithPassword pid=%d -> uid=%d (+pw)\n", conn.PID, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		default:
			return notImplemented(conn, ProtocolUtility, req)
		}
	}
}
