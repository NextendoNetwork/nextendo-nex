package nex

import "testing"

// Relecture r3 : GatheringIDByPID et SessionByPID rendaient la premiere session trouvee dans
// l'ordre aleatoire de la map ; un PID peut en tenir plusieurs (jusqu'a sessionsSeulMax ou il
// est seul, plus celle ou il est entre). Attendu : la session ou il n'est pas seul, sinon la
// plus recente, a chaque appel. PID fictifs 18009xxxxx.
func TestSessionDuPIDDeterministe(t *testing.T) {
	const pid, autre = uint64(1800900401), uint64(1800900402)
	m := NewMatchmaking()
	ajout := func(gid uint32, parts ...uint64) {
		sess := &MatchmakeSession{}
		sess.ID = gid
		m.gatherings[gid] = &gathering{session: sess, participants: parts}
	}
	for gid := uint32(1); gid <= 12; gid++ {
		ajout(gid, pid) // anciennes sessions ou il est seul
	}
	ajout(5, autre, pid) // la partie ou il est entre (gid ancien)
	ajout(20, autre)     // session d'un autre
	for i := 0; i < 200; i++ {
		if gid, ok := m.GatheringIDByPID(pid); !ok || gid != 5 {
			t.Fatalf("essai %d : GatheringIDByPID = %d, %v ; attendu 5 (la session ou il n'est pas seul)", i, gid, ok)
		}
		if p := m.SessionByPID(pid); len(p) != 2 || p[0] != autre || p[1] != pid {
			t.Fatalf("essai %d : SessionByPID = %v", i, p)
		}
	}
	delete(m.gatherings, 5)
	for i := 0; i < 200; i++ {
		if gid, ok := m.GatheringIDByPID(pid); !ok || gid != 12 {
			t.Fatalf("essai %d : seul partout, GatheringIDByPID = %d, %v ; attendu 12 (la plus recente)", i, gid, ok)
		}
	}
	if _, ok := m.GatheringIDByPID(1800900499); ok {
		t.Fatal("PID sans session : rien attendu")
	}
	if p := m.SessionByPID(1800900499); p != nil {
		t.Fatalf("PID sans session : %v", p)
	}
}
