package nex

import (
	"testing"
	"time"
)

// TestDepartSessionIsoleeSignale : dans une session isolee (match de tournoi SSBU, ModeIsole),
// le visiteur qui sort par EndParticipation (0x32.1) ou se deconnecte est signale au
// proprietaire (3008 / 3007), sinon l'hote le garde dans la composition du match et reste
// fige apres sa fermeture de participation (prod du 16/10, tournoi 892161984). Dans une
// session ordinaire, rien ne change (aucun avis sans NotifierDeparts).
func TestDepartSessionIsoleeSignale(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		mode   uint32
		deco   bool
		attend uint32 // type de notification attendu chez le proprietaire, 0 = aucune
	}{
		{0x54af74e1, false, NotificationEndParticipation},
		{0x54af74e1, true, NotificationParticipantDisconnected},
		{0x0000000c, false, 0},
		{0x0000000c, true, 0},
	} {
		ep := NewEndpoint(s)
		mm := NewMatchmaking()
		mm.Endpoint = ep
		ext := mm.ExtensionHandler()
		auto := func(conn *Connection) *MatchmakeSession {
			p := &AutoMatchmakeParam{Session: MatchmakeSession{
				Gathering:         Gathering{MaxParticipants: 2},
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
		hote, capHote := connCapte(ep, 5000, "88.0.0.1:1")
		visiteur := newTestConn(ep, 5001, "88.0.0.2:1")
		g := auto(hote)
		if auto(visiteur).ID != g.ID {
			t.Fatal("le visiteur doit rejoindre le salon de l'hote")
		}
		avant := len(capHote.rmc(t, s))
		if cas.deco {
			mm.RemovePlayer(visiteur.PID)
		} else {
			corps := NewStreamOut(s)
			corps.U32(g.ID)
			mm.MatchMakingExtHandler()(visiteur, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 2, corps.Bytes()))
		}
		msgs := capHote.attendre(t, s, avant+1, 200*time.Millisecond)[avant:]
		var types []uint32
		for _, m := range msgs {
			if m.Protocol != ProtocolNotifications {
				continue
			}
			var ev NotificationEvent
			NewStreamIn(m.Body, s).Extract(&ev)
			if ev.Param1 != uint64(g.ID) || ev.Param2 != visiteur.PID || ev.PIDSource != visiteur.PID {
				t.Fatalf("avis mal forme : %+v", ev)
			}
			types = append(types, ev.Type)
		}
		switch {
		case cas.attend == 0 && len(types) != 0:
			t.Fatalf("GameMode %#x : avis inattendu %v", cas.mode, types)
		case cas.attend != 0 && (len(types) != 1 || types[0] != cas.attend):
			t.Fatalf("GameMode %#x deco=%v : avis %v, %d attendu", cas.mode, cas.deco, types, cas.attend)
		}
	}
}
