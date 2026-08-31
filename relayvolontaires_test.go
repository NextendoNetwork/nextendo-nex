package nex

import "testing"

// TestRelaisFermeSansListe : la propriete qui compte. Arme, avec deux joueurs qui en ont
// besoin, mais sans liste — personne ne doit etre relaye. C est ce qui rend impossible de
// repeter le 2026-08-30, ou trois essais de suite ont touche des joueurs qui n avaient rien
// demande.
func TestRelaisFermeSansListe(t *testing.T) {
	resetBesoins()
	SetRelayVolontaires(nil, nil, false)
	SetPairRelay("127.0.0.1", 39900, 20)
	defer SetPairRelay("", 0, 0)

	NotePercage(70, false)
	NotePercage(70, false)
	NotePercage(71, false)
	NotePercage(71, false)

	if _, _, ok := PairRelayFor(70, 71, "1.1.1.1", "2.2.2.2"); ok {
		t.Fatal("relais accorde sans liste de volontaires")
	}
}

// TestRelaisRefuseSiUnSeulEstAutorise : on ne peut pas relayer un seul cote d une paire.
// Accepter sur un seul detournerait le trafic du partenaire, qui n a rien demande.
func TestRelaisRefuseSiUnSeulEstAutorise(t *testing.T) {
	resetBesoins()
	SetPairRelay("127.0.0.1", 40000, 20)
	defer SetPairRelay("", 0, 0)
	volontairesPourTest(t, 80) // 81 n est PAS autorise

	NotePercage(80, false)
	NotePercage(80, false)

	if _, _, ok := PairRelayFor(80, 81, "1.1.1.1", "2.2.2.2"); ok {
		t.Fatal("relais accorde alors qu un des deux n est pas volontaire")
	}
}

// TestListeVideeFermeLeRelais : perdre le fichier doit FERMER le relais, pas figer une liste
// ouverte. Un fichier efface par erreur ne doit pas laisser le relais tourner sur l ancienne.
func TestListeVideeFermeLeRelais(t *testing.T) {
	resetBesoins()
	SetPairRelay("127.0.0.1", 40100, 20)
	defer SetPairRelay("", 0, 0)
	SetRelayVolontaires([]uint64{90, 91}, nil, false)
	defer SetRelayVolontaires(nil, nil, false)

	NotePercage(90, false)
	NotePercage(90, false)

	if _, _, ok := PairRelayFor(90, 91, "1.1.1.1", "2.2.2.2"); !ok {
		t.Fatal("la paire autorisee aurait du etre relayee")
	}

	SetRelayVolontaires(nil, nil, false)
	if _, _, ok := PairRelayFor(90, 91, "1.1.1.1", "2.2.2.2"); ok {
		t.Fatal("liste videe : le relais doit se fermer")
	}
}

// TestForceContourneLeBesoinMaisPasLaListe : forcer sert a valider (un volontaire relaye
// cesse d accumuler des echecs et retomberait en direct en pleine session d essai), mais ne
// doit jamais elargir la portee.
func TestForceContourneLeBesoinMaisPasLaListe(t *testing.T) {
	resetBesoins()
	SetPairRelay("127.0.0.1", 40200, 20)
	defer SetPairRelay("", 0, 0)

	// Force et autorise : relaye sans avoir jamais echoue.
	SetRelayVolontaires([]uint64{100}, []uint64{100, 101}, false)
	defer SetRelayVolontaires(nil, nil, false)
	if _, _, ok := PairRelayFor(100, 101, "1.1.1.1", "2.2.2.2"); !ok {
		t.Fatal("une paire forcee doit etre relayee sans echec prealable")
	}

	// Force mais PAS autorise : refuse.
	SetRelayVolontaires(nil, nil, false)
	volMu.Lock()
	volForces = map[uint64]bool{110: true, 111: true}
	volMu.Unlock()
	defer SetRelayVolontaires(nil, nil, false)

	if _, _, ok := PairRelayFor(110, 111, "1.1.1.1", "2.2.2.2"); ok {
		t.Fatal("forcer ne doit pas contourner la liste")
	}
}
