package nex

import "testing"

// When a gathering's owner leaves it (EndParticipation) while a joiner is still in it, the
// joiner must take it over (flag 0x10) or it must go (flag unset) — never keep an absent owner
// (SSBU prod 10/17: InitiateProbe to the departed host -> 0x80010002, joiner waits up to 25 s).
func TestSortieProprietaire(t *testing.T) {
	for _, tc := range []struct {
		name          string
		optIn         bool
		flags         uint32
		wantGathering bool
		wantOwner     uint64 // 0 = unchanged departed owner
	}{
		{"flag 0x10: joiner takes over", true, 0x10 | 0xA00, true, 2000},
		{"flag unset: gathering deleted", true, 0, false, 0},
		{"not opted in: old behaviour", false, 0x10 | 0xA00, true, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings()
			ep := NewEndpoint(s)
			mm := NewMatchmaking()
			mm.SortieProprietaire = tc.optIn
			ext := mm.ExtensionHandler()
			auto := func(conn *Connection) uint32 {
				p := &AutoMatchmakeParam{Session: MatchmakeSession{
					Gathering:         Gathering{MaxParticipants: 2, Flags: tc.flags},
					GameMode:          1,
					OpenParticipation: true,
					Attribs:           []uint32{0, 0, 0, 0, 0, 0},
				}}
				body := NewStreamOut(s)
				body.Add(p)
				resp := ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
				var out MatchmakeSession
				NewStreamIn(resp.Body, s).Extract(&out)
				return out.ID
			}

			host := newTestConn(ep, 1000, "88.0.0.1:1")
			joiner := newTestConn(ep, 2000, "88.0.0.2:1")
			gid := auto(host)
			if got := auto(joiner); got != gid {
				t.Fatalf("joiner landed in %d, want host's gathering %d", got, gid)
			}

			body := NewStreamOut(s)
			body.U32(gid)
			mm.endParticipation(host, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 2, body.Bytes()))

			mm.mu.Lock()
			g := mm.gatherings[gid]
			mm.mu.Unlock()
			if (g != nil) != tc.wantGathering {
				t.Fatalf("gathering present=%v, want %v", g != nil, tc.wantGathering)
			}
			if g == nil {
				return
			}
			if g.session.OwnerPID != tc.wantOwner {
				t.Errorf("owner=%d, want %d", g.session.OwnerPID, tc.wantOwner)
			}
			if tc.wantOwner == joiner.PID {
				if g.hostConnID != joiner.ID || g.session.HostPID != joiner.PID {
					t.Errorf("host not moved: hostConnID=%d HostPID=%d", g.hostConnID, g.session.HostPID)
				}
				if len(g.participants) != 1 || g.participants[0] != joiner.PID {
					t.Errorf("participants=%v, want [%d]", g.participants, joiner.PID)
				}
			}
		})
	}
}
