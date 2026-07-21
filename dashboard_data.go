package nex

// This file exposes read-only snapshots of the live server state (gatherings and
// connections) so an external monitoring package (e.g. the mk8 dashboard) can
// render stats WITHOUT reaching into the store's internals or holding its locks.

// GatheringInfo is a read-only snapshot of one live gathering, for monitoring.
type GatheringInfo struct {
	ID           uint32
	OwnerPID     uint64
	HostPID      uint64
	Participants []uint64
	GameMode     uint32
	VR           uint32 // matchmake attribute[1] (the versus/battle rating searched with)
	MaxPart      uint16
	State        uint32
	Code         string // private-room code (empty for public sessions)
}

// Snapshot returns a copy of every live gathering. Safe to call concurrently.
func (m *Matchmaking) Snapshot() []GatheringInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GatheringInfo, 0, len(m.gatherings))
	for gid, g := range m.gatherings {
		if g == nil || g.session == nil {
			continue
		}
		var vr uint32
		if len(g.session.Attribs) > 1 {
			vr = g.session.Attribs[1]
		}
		out = append(out, GatheringInfo{
			ID:           gid,
			OwnerPID:     g.session.OwnerPID,
			HostPID:      g.session.HostPID,
			Participants: append([]uint64(nil), g.participants...),
			GameMode:     g.session.GameMode,
			VR:           vr,
			MaxPart:      g.session.MaxParticipants,
			State:        g.session.State,
			Code:         g.code,
		})
	}
	return out
}

// ConnInfo is a read-only snapshot of one live connection, for monitoring.
type ConnInfo struct {
	PID  uint64
	ID   uint32
	Addr string
}

// SnapshotConnections returns a copy of every live connection on the endpoint.
func (e *Endpoint) SnapshotConnections() []ConnInfo {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	out := make([]ConnInfo, 0, len(e.connections))
	for _, c := range e.connections {
		if c == nil {
			continue
		}
		out = append(out, ConnInfo{PID: c.PID, ID: c.ID, Addr: c.RemoteAddr})
	}
	return out
}
