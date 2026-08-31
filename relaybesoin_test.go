package nex

import (
	"testing"
	"time"
)

func resetBesoins() {
	besoinMu.Lock()
	besoins = map[uint64]*besoinEtat{}
	besoinNet = time.Time{}
	besoinMu.Unlock()
}

// TestUnSeulEchecNeSuffitPas : un echec isole arrive a tout le monde (pair parti, paquet
// perdu) et ne justifie pas de detourner tout le trafic d un joueur.
func TestUnSeulEchecNeSuffitPas(t *testing.T) {
	resetBesoins()
	NotePercage(1, false)
	if BesoinDeRelais(1) {
		t.Fatal("un seul echec ne doit pas rendre eligible")
	}
	NotePercage(1, false)
	if !BesoinDeRelais(1) {
		t.Fatal("deux echecs doivent rendre eligible")
	}
}

// TestUnSuccesEffaceLArdoise : le joueur vient de prouver qu il n a pas besoin du relais.
// Sans cela, un joueur qui branche un cable resterait detourne pendant un quart d heure.
func TestUnSuccesEffaceLArdoise(t *testing.T) {
	resetBesoins()
	NotePercage(2, false)
	NotePercage(2, false)
	if !BesoinDeRelais(2) {
		t.Fatal("doit etre eligible apres deux echecs")
	}
	NotePercage(2, true)
	if BesoinDeRelais(2) {
		t.Fatal("un succes doit effacer l eligibilite")
	}
}

// TestEchecsTropVieux : au-dela de la fenetre, un echec ne dit plus rien de l etat actuel
// du reseau du joueur.
func TestEchecsTropVieux(t *testing.T) {
	resetBesoins()
	NotePercage(3, false)
	NotePercage(3, false)

	besoinMu.Lock()
	besoins[3].dernier = time.Now().Add(-besoinFenetre - time.Minute)
	besoinMu.Unlock()

	if BesoinDeRelais(3) {
		t.Fatal("un echec hors fenetre ne doit plus rendre eligible")
	}
}

// TestRelaisRefuseSansBesoin : la porte d entree elle-meme. Deux joueurs qui n ont jamais
// echoue ne doivent PAS recevoir de port de relais.
func TestRelaisRefuseSansBesoin(t *testing.T) {
	resetBesoins()
	SetPairRelay("127.0.0.1", 39500, 50)
	defer SetPairRelay("", 0, 0)

	if _, _, ok := PairRelayFor(10, 11, "1.1.1.1", "2.2.2.2"); ok {
		t.Fatal("relais accorde a une paire sans besoin")
	}

	// Un seul des deux suffit : le percage est une operation a deux.
	NotePercage(11, false)
	NotePercage(11, false)
	if _, _, ok := PairRelayFor(10, 11, "1.1.1.1", "2.2.2.2"); !ok {
		t.Fatal("relais refuse alors qu un des deux en a besoin")
	}
}
