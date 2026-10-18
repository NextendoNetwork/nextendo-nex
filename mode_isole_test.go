package nex

import "testing"

// TestModeIsoleNonScindeParLeNAT : un match de tournoi SSBU passe par un GameMode isole
// 0x54xxxxxx (super-smash-bros-ultimate, tournoi_matchmaking.go). Il n'existe qu'UNE session
// possible pour ses joueurs : le filtre NAT d'autoMatchmake ne doit pas la scinder, sinon
// l'arrivant ouvre son propre salon et attend seul un adversaire qui ne viendra jamais.
// Pour un GameMode ordinaire, le filtre reste en place (deux NAT stricts restent separes).
func TestModeIsoleNonScindeParLeNAT(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		mode     uint32
		ensemble bool
	}{
		{0x54a1b2c3, true},  // match de tournoi : meme salon malgre deux NAT stricts
		{0x00000005, false}, // partie rapide : le filtre NAT separe toujours
	} {
		ep := NewEndpoint(s)
		mm := NewMatchmaking()
		ext := mm.ExtensionHandler()
		auto := func(conn *Connection) *MatchmakeSession {
			p := &AutoMatchmakeParam{Session: MatchmakeSession{
				Gathering:         Gathering{MaxParticipants: 4},
				GameMode:          cas.mode,
				OpenParticipation: true,
				Attribs:           []uint32{0, 0, 0, 0, 0, 0},
			}}
			body := NewStreamOut(s)
			body.Add(p)
			resp := ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
			var out MatchmakeSession
			NewStreamIn(resp.Body, s).Extract(&out)
			return &out
		}
		hote := newTestConn(ep, 4000, "88.0.0.1:1")
		arrivant := newTestConn(ep, 4001, "88.0.0.2:1")
		for _, c := range []*Connection{hote, arrivant} {
			c.natMapV.Store(2) // natm 2 : NAT strict
			c.natFilterV.Store(2)
		}
		a := auto(hote)
		b := auto(arrivant)
		if (a.ID == b.ID) != cas.ensemble {
			t.Fatalf("GameMode %#x : salons %d et %d (ensemble attendu : %v)", cas.mode, a.ID, b.ID, cas.ensemble)
		}
	}
	if !ModeIsole(0x54000000) || !ModeIsole(0x54ffffff) || ModeIsole(0x55000000) || ModeIsole(0x00540000) {
		t.Fatal("ModeIsole : seul le prefixe 0x54 sur l'octet de poids fort")
	}
}
