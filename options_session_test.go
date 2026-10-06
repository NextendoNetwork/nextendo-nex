package nex

import "testing"

// TestSessionTrouveeRespecteLeChoixDuJeu : le recadrage d'une session trouvee selon les
// options de resultat (mesure sur SMM2) ne s'applique qu'aux jeux qui l'ont demande.
// Les autres — ACNH et ses visites d'ile — recoivent la session complete, comme avant.
func TestSessionTrouveeRespecteLeChoixDuJeu(t *testing.T) {
	src := &MatchmakeSession{ApplicationData: []byte{1, 2, 3}}
	src.Param.Params = map[string]Variant{"@RV": {Type: VariantInt64, Int: 5}}

	defaut := &Matchmaking{}
	if r := defaut.sessionTrouvee(src, 0); len(r.ApplicationData) != 3 || len(r.Param.Params) != 1 {
		t.Fatalf("sans l'option, la session doit rester complete : %+v", r)
	}
	smm2 := &Matchmaking{FindByParticipantHonorOptions: true}
	if r := smm2.sessionTrouvee(src, 0); r.ApplicationData != nil || r.Param.Params != nil {
		t.Fatalf("avec l'option et aucun bit, donnees et parametres retires : %+v", r)
	}
	if r := smm2.sessionTrouvee(src, 1); len(r.ApplicationData) != 3 || r.Param.Params != nil {
		t.Fatalf("bit 0 seul : donnees gardees, parametres retires : %+v", r)
	}
	if len(src.ApplicationData) != 3 {
		t.Fatal("la session d'origine a ete modifiee")
	}
}
