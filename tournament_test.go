package nex

import "testing"

// La méthode 0x3A est partagée : sonde d'avant-match pour Super Smash Bros, et
// « donne-moi ces tournois » pour Mario Kart. On les distingue à la forme de la
// requête ; ce test fige cette frontière, parce qu'une confusion ici renvoie au
// jeu un nombre d'entrées qui n'existent pas.
func TestIsTournamentIDList(t *testing.T) {
	// Requête réelle de Mario Kart, relevée sur la capture : 85 identifiants,
	// donc 4 + 85×4 = 344 octets.
	mk8 := make([]byte, 4+85*4)
	mk8[0] = 85
	if !isTournamentIDList(mk8) {
		t.Error("la liste d'ids de Mario Kart devrait être reconnue")
	}

	cases := []struct {
		nom  string
		body []byte
		veut bool
	}{
		{"corps vide (sonde SSBU)", nil, false},
		{"trop court", []byte{1, 0, 0, 0}, false},
		{"nombre nul", []byte{0, 0, 0, 0, 0, 0, 0, 0}, false},
		{"longueur incohérente", []byte{2, 0, 0, 0, 1, 0, 0, 0}, false},
		{"deux identifiants", []byte{2, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0}, true},
		{"non multiple de 4", []byte{1, 0, 0, 0, 1, 0, 0, 0, 9}, false},
		{"nombre absurde", []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}, false},
	}
	for _, c := range cases {
		if got := isTournamentIDList(c.body); got != c.veut {
			t.Errorf("%s : %v, attendu %v", c.nom, got, c.veut)
		}
	}
}

// Les méthodes du chemin tournoi doivent RÉPONDRE. Laissées sans réponse elles
// renvoient une erreur, et le jeu abandonne l'écran avant même toute donnée.
func TestTournamentMethodsAnswer(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "1.1.1.1:1", func([]byte) {})

	// Ranking 0x10 : classement d'un tournoi. Nintendo rend une liste vide
	// (4 octets) quand il n'y a rien — on doit faire pareil, jamais une erreur.
	r := RankingHandler()(conn, NewRMCRequest(s, ProtocolRanking, methodRankingCompetitionRanking, 1, nil))
	if r.IsError {
		t.Fatalf("classement de tournoi : erreur renvoyée (%+v)", r)
	}
	if len(r.Body) != 4 || NewStreamIn(r.Body, s).U32() != 0 {
		t.Errorf("classement de tournoi : corps %x, attendu une liste vide", r.Body)
	}

	// Ranking 0x12 : liste des tournois. On valide la FORME, pas le nombre : le
	// magasin est global et d'autres tests y créent des tournois.
	l := RankingHandler()(conn, NewRMCRequest(s, ProtocolRanking, methodRankingGetCompetitionInfo, 2, nil))
	if l.IsError || len(l.Body) < 4 {
		t.Fatalf("liste des tournois : %+v", l)
	}
	n := NewStreamIn(l.Body, s).U32()
	if int(n)*33+4 != len(l.Body) {
		t.Errorf("%d tournoi(s) annonce(s) mais %d octets : entrees de 33 octets attendues", n, len(l.Body))
	}
}
