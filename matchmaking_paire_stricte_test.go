package nex

import (
	"fmt"
	"testing"
)

// autoSalon : AutoMatchmakeWithParamPostpone pour un salon a 4, rend la session obtenue.
func autoSalon(t *testing.T, mm *Matchmaking, conn *Connection, mode uint32) *MatchmakeSession {
	t.Helper()
	s := conn.Settings
	p := &AutoMatchmakeParam{Session: MatchmakeSession{
		Gathering:         Gathering{MaxParticipants: 4},
		GameMode:          mode,
		OpenParticipation: true,
		Attribs:           []uint32{0, 0, 0, 0, 0, 0},
	}}
	body := NewStreamOut(s)
	body.Add(p)
	resp := mm.ExtensionHandler()(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
	var out MatchmakeSession
	NewStreamIn(resp.Body, s).Extract(&out)

	return &out
}

// joueurNATm : une connexion enregistree dont la station annonce ce natm.
func joueurNATm(ep *Endpoint, pid uint64, natm int) *Connection {
	c := newTestConn(ep, pid, fmt.Sprintf("198.51.100.%d:1", pid%200+1))
	u := NewStationURL("prudp")
	u.SetInt("natm", natm)
	c.SetStations([]*StationURL{u})

	return c
}

// Deux salons de Partie rapide conviennent ; dans l'un, un membre NON hote forme avec
// l'arrivant une paire natm 1 / natm 2 (0515/0516), que le relais ne peut pas couvrir.
// L'arrivant prend l'autre, quel que soit l'ordre de parcours de la carte.
func TestPrefereSalonSansPaireStricteHorsHote(t *testing.T) {
	for essai := 0; essai < 30; essai++ {
		ep := NewEndpoint(testSettings())
		mm := NewMatchmaking()
		hoteA := joueurNATm(ep, 1800900201, 1)
		salonA := autoSalon(t, mm, hoteA, 11)
		membreA := joueurNATm(ep, 1800900202, 2) // paire avec l'hote : permise
		if got := autoSalon(t, mm, membreA, 11); got.ID != salonA.ID {
			t.Fatalf("le membre natm 2 doit rejoindre A (paire avec l'hote seulement)")
		}
		hoteB := joueurNATm(ep, 1800900203, 1)
		salonB := autoSalon(t, mm, hoteB, 12)
		membreB := joueurNATm(ep, 1800900204, 1)
		autoSalon(t, mm, membreB, 12)
		mm.mu.Lock()
		mm.gatherings[salonB.ID].session.GameMode = 11
		mm.mu.Unlock()

		arrivant := joueurNATm(ep, 1800900205, 1)
		if got := autoSalon(t, mm, arrivant, 11); got.ID != salonB.ID {
			t.Fatalf("essai %d : l'arrivant natm 1 devait rejoindre B (%d), il a pris %d", essai, salonB.ID, got.ID)
		}
	}
}

// La preference ne devient jamais un refus : seul salon disponible, l'arrivant y entre
// malgre la paire stricte hors hote. Et une paire stricte avec l'HOTE seul ne compte pas
// (elle passe par le relais).
func TestPaireStricteHorsHoteSertDeRepli(t *testing.T) {
	ep := NewEndpoint(testSettings())
	mm := NewMatchmaking()
	hote := joueurNATm(ep, 1800900211, 1)
	salon := autoSalon(t, mm, hote, 11)
	membre := joueurNATm(ep, 1800900212, 2)
	autoSalon(t, mm, membre, 11)
	arrivant := joueurNATm(ep, 1800900213, 1)
	if got := autoSalon(t, mm, arrivant, 11); got.ID != salon.ID {
		t.Fatalf("faute de mieux l'arrivant devait rejoindre %d, il a pris %d", salon.ID, got.ID)
	}

	ep2 := NewEndpoint(testSettings())
	mm2 := NewMatchmaking()
	hoteStrict := joueurNATm(ep2, 1800900214, 2)
	salon2 := autoSalon(t, mm2, hoteStrict, 11)
	mm2.mu.Lock()
	evite := paireStricteHorsHote(ep2, joueurNATm(ep2, 1800900215, 1), mm2.gatherings[salon2.ID])
	mm2.mu.Unlock()
	if evite {
		t.Fatal("paire stricte avec l'hote seul : ne doit pas compter")
	}
	// natm inconnu de l'arrivant : aucune paire stricte.
	inconnu := joueurNATm(ep, 1800900216, 0)
	mm.mu.Lock()
	evite = paireStricteHorsHote(ep, inconnu, mm.gatherings[salon.ID])
	mm.mu.Unlock()
	if evite {
		t.Fatal("natm inconnu : ne doit pas compter")
	}
}

// Un salon a paire stricte hors hote (hote en ligne) passe avant un salon a hote absent.
func TestPaireStricteAvantHoteAbsent(t *testing.T) {
	for essai := 0; essai < 30; essai++ {
		ep := NewEndpoint(testSettings())
		mm := NewMatchmaking()
		hoteA := joueurNATm(ep, 1800900221, 1)
		salonA := autoSalon(t, mm, hoteA, 11)
		autoSalon(t, mm, joueurNATm(ep, 1800900222, 2), 11)
		absent := joueurNATm(ep, 1800900223, 1)
		salonB := autoSalon(t, mm, absent, 12)
		mm.mu.Lock()
		mm.gatherings[salonB.ID].session.GameMode = 11
		mm.mu.Unlock()
		ep.unregisterConnection(absent)

		if got := autoSalon(t, mm, joueurNATm(ep, 1800900224, 1), 11); got.ID != salonA.ID {
			t.Fatalf("essai %d : salon a paire stricte (hote en ligne) %d attendu, pris %d", essai, salonA.ID, got.ID)
		}
	}
}
