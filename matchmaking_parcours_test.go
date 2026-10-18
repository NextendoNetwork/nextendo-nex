package nex

import "testing"

// TestParcoursArenesFiltre : la recherche d'arenes du jeu (vecteurs critere_arene_*.hex,
// 0x168b240 genre 7) ne rend, filtre actif, que les arenes du GameMode 7 dont les attributs
// valent les filtres ; jamais un salon de Partie rapide qui attend (GameMode 1). Filtre coupe :
// toute session ouverte, comme avant. Une recherche par mot de code garde sa regle.
func TestParcoursArenesFiltre(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		vecteur string
		actif   bool
		attendu int
	}{
		{"critere_arene_filtres", true, 1},     // arene [2 1 0 2], sans mot de passe
		{"critere_arene_filtres", false, 3},    // les 3 sessions sans mot de passe
		{"critere_arene_sans_filtre", true, 3}, // 3 arenes (dont la protegee), pas la Partie rapide
		{"critere_arene_sans_filtre", false, 4},
	} {
		ep := NewEndpoint(s)
		mm := NewMatchmaking()
		if cas.actif {
			mm.FiltreParcours = func(*Connection) bool { return true }
		}
		creer := func(pid uint64, mode uint32, attribs []uint32, mdp string, auto bool) {
			c := newTestConn(ep, pid, "198.51.100.1:1")
			p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, GameMode: mode,
				OpenParticipation: true, MatchmakeSystem: 1, Attribs: attribs, UserPassword: mdp}}
			o := NewStreamOut(s)
			o.Add(p)
			meth := MethodCreateMatchmakeSessionWithParam
			if auto {
				meth = MethodAutoMatchmakeWithParamPostpone
			}
			mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, meth, 1, o.Bytes()))
		}
		creer(1, 7, []uint32{2, 1, 0, 2, 0, 0}, "", false)
		creer(2, 7, []uint32{2, 3, 0, 2, 0, 0}, "", false)
		creer(3, 7, []uint32{2, 1, 0, 2, 0, 0}, "1234", false)
		creer(4, 1, []uint32{2, 11, 0, 0, 0, 0}, "", true)
		chercheur := newTestConn(ep, 9, "198.51.100.9:1")
		rep := mm.ExtensionHandler()(chercheur, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 2, vecteurAppariement(t, cas.vecteur)))
		in := NewStreamIn(rep.Body, s)
		n := int(in.U32())
		if rep.IsError || n != cas.attendu {
			t.Fatalf("%s actif=%v : %d session(s), %d attendue(s)", cas.vecteur, cas.actif, n, cas.attendu)
		}
	}
}

// TestParcoursCodeNonFiltre : avec un mot de code (tournoi, Dodo Code), le filtre ne s'applique
// pas (la session isolee d'un tournoi a un GameMode remplace, ses criteres non).
func TestParcoursCodeNonFiltre(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.FiltreParcours = func(*Connection) bool { return true }
	c := newTestConn(ep, 1, "198.51.100.1:1")
	p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 2}, GameMode: 0x54000001,
		OpenParticipation: true, Codeword: "T-0000000001-00001"}}
	o := NewStreamOut(s)
	o.Add(p)
	mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCreateMatchmakeSessionWithParam, 1, o.Bytes()))
	cr := critereDuJeu(t, "critere_arene_sans_filtre")
	cr.Codeword = "T-0000000001-00001"
	cr.GameMode = "12"
	q := NewStreamOut(s)
	q.Add(&cr)
	rep := mm.ExtensionHandler()(newTestConn(ep, 2, "198.51.100.2:1"), NewRMCRequest(s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 2, q.Bytes()))
	if n := NewStreamIn(rep.Body, s).U32(); n != 1 {
		t.Fatalf("recherche par code : %d session(s)", n)
	}
}
