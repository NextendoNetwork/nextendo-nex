package nex

import "testing"

func gatheringCount(m *Matchmaking) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.gatherings)
}

func hasGathering(m *Matchmaking, gid uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.gatherings[gid]
	return ok
}

// Le cas mesure sur Strikers : createSession puis autoMatchmake, sans quitter le premier
// salon. Le salon laisse seul derriere soi doit disparaitre.
func TestAbandonedSoloGatheringIsReleased(t *testing.T) {
	m := NewMatchmaking()
	m.mu.Lock()
	m.gatherings[1] = &gathering{session: &MatchmakeSession{}, participants: []uint64{1800003406}}
	m.nextGID = 2
	m.releaseAbandonedSolo(1800003406, 2)
	m.mu.Unlock()

	if hasGathering(m, 1) {
		t.Fatal("le salon solo abandonne aurait du etre supprime")
	}
}

// Un salon ou d'autres joueurs sont presents est une vraie partie : on n'y touche pas.
func TestPopulatedGatheringSurvives(t *testing.T) {
	m := NewMatchmaking()
	m.mu.Lock()
	m.gatherings[1] = &gathering{session: &MatchmakeSession{}, participants: []uint64{1800003406, 1800033784}}
	m.releaseAbandonedSolo(1800003406, 2)
	m.mu.Unlock()

	if !hasGathering(m, 1) {
		t.Fatal("un salon avec d'autres joueurs ne doit jamais etre supprime")
	}
}

// Le salon qu'on vient de creer ne doit evidemment pas se supprimer lui-meme.
func TestNewGatheringIsKept(t *testing.T) {
	m := NewMatchmaking()
	m.mu.Lock()
	m.gatherings[7] = &gathering{session: &MatchmakeSession{}, participants: []uint64{42}}
	m.releaseAbandonedSolo(42, 7)
	m.mu.Unlock()

	if !hasGathering(m, 7) {
		t.Fatal("le nouveau salon a ete supprime")
	}
}

// Le salon solo d'un AUTRE joueur ne bouge pas.
func TestOtherPlayersSoloGatheringUntouched(t *testing.T) {
	m := NewMatchmaking()
	m.mu.Lock()
	m.gatherings[1] = &gathering{session: &MatchmakeSession{}, participants: []uint64{999}}
	m.releaseAbandonedSolo(42, 2)
	m.mu.Unlock()

	if !hasGathering(m, 1) {
		t.Fatal("le salon d'un autre joueur a ete supprime")
	}
}

// Le code de salon prive doit partir avec le salon, sinon il resterait trouvable.
func TestAbandonedSoloGatheringDropsItsCode(t *testing.T) {
	m := NewMatchmaking()
	m.mu.Lock()
	m.gatherings[1] = &gathering{session: &MatchmakeSession{}, participants: []uint64{42}, code: "ABCD"}
	m.byCode["ABCD"] = 1
	m.releaseAbandonedSolo(42, 2)
	_, codeStillThere := m.byCode["ABCD"]
	m.mu.Unlock()

	if codeStillThere {
		t.Fatal("le code du salon supprime est reste dans l'index")
	}
}

// Bout en bout : deux createGathering successifs pour le meme joueur ne laissent qu'un salon.
func TestSecondCreateLeavesOneGathering(t *testing.T) {
	m := NewMatchmaking()
	conn := &Connection{PID: 1800003406, ID: 1}

	m.mu.Lock()
	g1 := m.createGathering(conn, &MatchmakeSession{})
	m.mu.Unlock()

	m.mu.Lock()
	g2 := m.createGathering(conn, &MatchmakeSession{})
	m.mu.Unlock()

	if g1.session.Gathering.ID == g2.session.Gathering.ID {
		t.Fatal("les deux salons devraient avoir des identifiants distincts")
	}
	if n := gatheringCount(m); n != 1 {
		t.Fatalf("%d salons restants, attendu 1", n)
	}
	if !hasGathering(m, g2.session.Gathering.ID) {
		t.Fatal("c'est le salon le PLUS RECENT qui doit survivre")
	}
}
