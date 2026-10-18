package nex

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Vecteurs : octets produits par le VRAI code du jeu (main 13.0.5) en emulation,
// un outil interne : reglage Smash -> 0x3707990 -> 0x113d70
// -> 0x255f00 (critere_*.hex), et la requete 0x6D.0x28 entiere par 0x252520 (auto_*.hex).
func vecteurAppariement(t *testing.T, nom string) []byte {
	t.Helper()
	h, err := os.ReadFile(filepath.Join("testdata", "appariement", nom+".hex"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(h)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func critereDuJeu(t *testing.T, nom string) MatchmakeSessionSearchCriteria {
	t.Helper()
	var c MatchmakeSessionSearchCriteria
	in := NewStreamIn(vecteurAppariement(t, nom), testSettings())
	in.Extract(&c)
	if in.Err() != nil || in.Remaining() != 0 {
		t.Fatalf("%s : lecture err=%v reste=%d", nom, in.Err(), in.Remaining())
	}
	return c
}

// TestCritereDuJeuLu : le critere de la Partie rapide tel que le jeu l'ecrit.
func TestCritereDuJeuLu(t *testing.T) {
	c := critereDuJeu(t, "critere_rapide_passe1")
	if c.GameMode != "1" || c.MatchmakeSystem != "1" || c.MinParticipants != "" || c.MaxParticipants != "" {
		t.Fatalf("champs : mode=%q sys=%q min=%q max=%q", c.GameMode, c.MatchmakeSystem, c.MinParticipants, c.MaxParticipants)
	}
	if strings.Join(c.Attribs, "|") != "2|10,12|0|||" {
		t.Fatalf("attribs %q", c.Attribs)
	}
	if !c.VacantOnly || !c.ExcludeLocked || c.VacantParticipants != 1 || c.SelectionMethod != 5 || c.ExcludeUserPassword {
		t.Fatalf("drapeaux %+v", c)
	}
	p := c.Param.Params
	if p["@RV"].Uint != 1500 || p["@SI"].Uint != 1 || p["_RU"].Uint != 0x10003 || p["@CC"].String != "FR" || !p["@UsGI"].Bool {
		t.Fatalf("parametres %s", texteParam(p))
	}
	if e := critereDuJeu(t, "critere_elite_passe2"); strings.Join(e.Attribs, "|") != "2|9,13|0,1|||" {
		t.Fatalf("elite, seconde passe : %q", e.Attribs)
	}
	if z := critereDuJeu(t, "critere_rapide_tranche0"); z.Attribs[1] != "0,1" {
		t.Fatalf("tranche 0 : %q", z.Attribs[1])
	}
}

// TestRequete28EntiereLue : la requete 0x6D.0x28 entiere (0x252520), et l'ancienne lecture de
// la seule session reste valable.
func TestRequete28EntiereLue(t *testing.T) {
	s := testSettings()
	for nom, maxP := range map[string]uint16{"auto_1v1": 2, "auto_smash4": 4, "auto_elite": 2} {
		b := vecteurAppariement(t, nom)
		p, ok := lireAutoMatchmakeComplet(b, s)
		if !ok {
			t.Fatalf("%s illisible", nom)
		}
		if p.Session.MaxParticipants != maxP || len(p.Criteres) != 1 || p.ParticipationCount != 1 || len(p.Participants) != 0 || len(p.GIDsCibles) != 0 {
			t.Fatalf("%s : maxP=%d criteres=%d count=%d", nom, p.Session.MaxParticipants, len(p.Criteres), p.ParticipationCount)
		}
		var ancien AutoMatchmakeParam
		in := NewStreamIn(b, s)
		in.Extract(&ancien)
		if in.Err() != nil || ancien.Session.MaxParticipants != maxP {
			t.Fatalf("%s : lecture de la session seule err=%v", nom, in.Err())
		}
		// Ecriture Go de meme longueur (l'ordre des cles des parametres, une table, peut differer).
		o := NewStreamOut(s)
		o.Add(&p)
		if len(o.Bytes()) != len(b) {
			t.Fatalf("%s : reecriture de %d octets, %d attendus", nom, len(o.Bytes()), len(b))
		}
	}
}

func TestCritereValeur(t *testing.T) {
	for _, c := range []struct {
		c      string
		v      uint32
		ok, cn bool
	}{
		{"", 7, true, true}, {"7", 7, true, true}, {"7", 8, false, true},
		{"10,12", 10, true, true}, {"10,12", 12, true, true}, {"10,12", 13, false, true}, {"10,12", 9, false, true},
		{"x", 1, true, false}, {"1,y", 1, true, false},
	} {
		if ok, cn := critereValeur(c.c, c.v); ok != c.ok || cn != c.cn {
			t.Errorf("%q %d -> %v %v", c.c, c.v, ok, cn)
		}
	}
}

// Banc d'essai : sessions et criteres du jeu, requete 0x6D.0x28 ecrite par Go.
type bancAppariement struct {
	t   *testing.T
	s   *Settings
	ep  *Endpoint
	mm  *Matchmaking
	ext RMCHandler
	pal *PaliersAppariement
}

func nouveauBanc(t *testing.T, actif bool) *bancAppariement {
	b := &bancAppariement{t: t, s: testSettings(), mm: NewMatchmaking()}
	b.ep = NewEndpoint(b.s)
	b.ext = b.mm.ExtensionHandler()
	b.pal = &PaliersAppariement{AttributNiveau: 1, Niveau: 10 * time.Second, Format: 20 * time.Second, Tout: 30 * time.Second}
	if actif {
		b.mm.Appariement = func(*Connection) *PaliersAppariement { return b.pal }
	}
	return b
}

// chercher : 0x6D.0x28 de pid avec la session et le critere du vecteur nom, tranche et Elite
// remplaces (attribs du critere : plage[1], elite[2]).
func (b *bancAppariement) chercher(pid uint64, nom string, tranche uint32, plage, elite string) uint32 {
	b.t.Helper()
	p, ok := lireAutoMatchmakeComplet(vecteurAppariement(b.t, nom), b.s)
	if !ok {
		b.t.Fatal(nom)
	}
	p.Session.Attribs[1] = tranche
	if elite == "1" {
		p.Session.Attribs[2] = 1
	} else {
		p.Session.Attribs[2] = 0
	}
	p.Criteres[0].Attribs[1] = plage
	p.Criteres[0].Attribs[2] = elite
	o := NewStreamOut(b.s)
	o.Add(&p)
	c := b.ep.FindConnectionByPID(pid)
	if c == nil {
		c = newTestConn(b.ep, pid, "198.51.100.1:1")
	}
	resp := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes()))
	var out MatchmakeSession
	in := NewStreamIn(resp.Body, b.s)
	in.Extract(&out)
	if resp.IsError || in.Err() != nil {
		b.t.Fatalf("reponse 0x28 : %v", in.Err())
	}
	return out.ID
}

// vieillir : le salon gid a ete cree il y a d.
func (b *bancAppariement) vieillir(gid uint32, d time.Duration) {
	b.mm.mu.Lock()
	b.mm.gatherings[gid].cree = time.Now().Add(-d)
	b.mm.mu.Unlock()
}

// TestAppariementFormat : en refus strict (RefusDes, la logique des paliers seule), 1 contre 1
// et Smash a 4 ne se melangent pas tant que l'attente est courte ; passe le palier Format, le
// joueur de Smash a 4 rejoint le salon 1 contre 1.
func TestAppariementFormat(t *testing.T) {
	b := nouveauBanc(t, true)
	b.pal.RefusDes = 1
	h := b.chercher(1, "auto_1v1", 11, "10,12", "0")
	j := b.chercher(2, "auto_smash4", 11, "9,13", "0")
	if j == h {
		t.Fatalf("Smash a 4 a rejoint tout de suite le salon 1 contre 1 %d", h)
	}
	if k := b.chercher(3, "auto_1v1", 11, "10,12", "0"); k != h {
		t.Fatalf("le 1 contre 1 devait rejoindre %d, il a pris %d", h, k)
	}
	h2 := b.chercher(4, "auto_1v1", 12, "11,13", "0")
	b.vieillir(h2, 25*time.Second)
	b.vieillir(j, 0)
	if x := b.chercher(5, "auto_smash4", 12, "11,13", "0"); x != j {
		t.Fatalf("Smash a 4 devait rejoindre son format %d avant le salon 1 contre 1 qui attend, il a pris %d", j, x)
	}
	if y := b.chercher(6, "auto_smash4", 12, "11,13", "0"); y != j {
		t.Fatalf("salon Smash a 4 encore ouvert (%d) : %d", j, y)
	}
	// Plus de salon Smash a 4 libre que celui-ci une fois plein : le salon 1 contre 1 qui
	// attend depuis 25 s (palier Format) est pris.
	b.mm.mu.Lock()
	b.mm.gatherings[j].session.OpenParticipation = false
	b.mm.mu.Unlock()
	if z := b.chercher(7, "auto_smash4", 12, "11,13", "0"); z != h2 {
		t.Fatalf("passe 20 s, le salon 1 contre 1 %d devait etre pris, obtenu %d", h2, z)
	}
}

// TestAppariementNiveau : en refus strict, une tranche hors de la plage du critere attend le
// palier Niveau, compte depuis le salon ou depuis la serie du joueur.
func TestAppariementNiveau(t *testing.T) {
	b := nouveauBanc(t, true)
	b.pal.RefusDes = 1
	h := b.chercher(1, "auto_1v1", 11, "10,12", "0")
	if j := b.chercher(2, "auto_1v1", 16, "15,17", "0"); j == h {
		t.Fatal("tranche 16 entree tout de suite dans un salon de tranche 11")
	}
	// Le second joueur quitte son salon et recherche, sa serie a 12 s : le salon de tranche 11
	// lui est permis.
	b.mm.RemovePlayer(2)
	b.mm.mu.Lock()
	b.mm.series[2] = time.Now().Add(-12 * time.Second)
	b.mm.mu.Unlock()
	b.vieillir(h, 0)
	if j := b.chercher(2, "auto_1v1", 16, "15,17", "0"); j != h {
		t.Fatalf("apres 12 s de recherche, le salon %d devait etre pris, obtenu %d", h, j)
	}
}

// TestAppariementElite : en refus strict, la seconde recherche d'un joueur Elite accepte une
// session non Elite (critere [0,1]) ; un joueur non Elite n'entre dans une session Elite qu'au
// dernier palier.
func TestAppariementElite(t *testing.T) {
	b := nouveauBanc(t, true)
	b.pal.RefusDes = 1
	e := b.chercher(1, "auto_elite", 11, "10,12", "1")
	if n := b.chercher(2, "auto_1v1", 11, "10,12", "0"); n == e {
		t.Fatal("non Elite entre tout de suite dans une session Elite")
	}
	b2 := nouveauBanc(t, true)
	n := b2.chercher(1, "auto_1v1", 11, "10,12", "0")
	if e := b2.chercher(2, "auto_elite", 11, "9,13", "0,1"); e != n {
		t.Fatalf("Elite, seconde passe : devait rejoindre %d, obtenu %d", n, e)
	}
	b3 := nouveauBanc(t, true)
	b3.pal.RefusDes = 1
	e3 := b3.chercher(1, "auto_elite", 11, "10,12", "1")
	b3.vieillir(e3, 31*time.Second)
	if n3 := b3.chercher(2, "auto_1v1", 11, "10,12", "0"); n3 != e3 {
		t.Fatalf("passe 30 s, comportement d'avant : devait rejoindre %d, obtenu %d", e3, n3)
	}
}

// TestAppariementFauteDeMieux : par defaut (RefusDes 0), un salon refuse est pris faute de
// mieux, jamais un salon de plus (pas de fragmentation) ; un salon permis passe avant un salon
// refuse, puis le palier le plus bas.
func TestAppariementFauteDeMieux(t *testing.T) {
	// Seul salon : autre format, autre tranche, Elite contre non Elite (palier 3, attente nulle).
	b := nouveauBanc(t, true)
	h := b.chercher(1, "auto_1v1", 11, "10,12", "0")
	if j := b.chercher(2, "auto_smash4", 16, "15,17", "1"); j != h {
		t.Fatalf("seul salon ouvert %d : l'arrivant devait y entrer, il a ouvert %d", h, j)
	}

	// A : meme format, autre tranche, cree a l'instant (palier 1, pas encore permis).
	// B : autre format, meme tranche, 21 s d'attente (palier 2, permis passe 20 s).
	b = nouveauBanc(t, true)
	b.pal.RefusDes = 1 // mise en place : chacun ouvre son salon
	a := b.chercher(1, "auto_1v1", 16, "15,17", "0")
	bb := b.chercher(2, "auto_smash4", 11, "9,13", "0")
	if a == bb {
		t.Fatal("mise en place : un seul salon")
	}
	b.vieillir(a, 0)
	b.vieillir(bb, 21*time.Second)
	b.pal.RefusDes = 0
	if x := b.chercher(3, "auto_1v1", 11, "10,12", "0"); x != bb {
		t.Fatalf("le salon permis %d devait passer avant le salon refuse %d, obtenu %d", bb, a, x)
	}

	// Les deux refuses : le palier le plus bas (A, palier 1) l'emporte.
	b = nouveauBanc(t, true)
	b.pal.RefusDes = 1
	a = b.chercher(1, "auto_1v1", 16, "15,17", "0")
	bb = b.chercher(2, "auto_smash4", 11, "9,13", "0")
	b.vieillir(a, 0)
	b.vieillir(bb, 0)
	b.pal.RefusDes = 0
	if x := b.chercher(3, "auto_1v1", 11, "10,12", "0"); x != a {
		t.Fatalf("deux salons refuses : le palier 1 (%d) devait l'emporter sur le palier 2 (%d), obtenu %d", a, bb, x)
	}

	// Refus strict a partir de 3 salons evalues : avec 1 seul salon, il est pris.
	b = nouveauBanc(t, true)
	b.pal.RefusDes = 3
	h = b.chercher(1, "auto_1v1", 11, "10,12", "0")
	if j := b.chercher(2, "auto_1v1", 16, "15,17", "0"); j != h {
		t.Fatalf("RefusDes 3, un seul salon : devait entrer dans %d, obtenu %d", h, j)
	}
}

// TestAppariementCoupe : sans Appariement, rien ne change (le format n'est pas regarde).
func TestAppariementCoupe(t *testing.T) {
	b := nouveauBanc(t, false)
	h := b.chercher(1, "auto_1v1", 11, "10,12", "0")
	if j := b.chercher(2, "auto_smash4", 16, "15,17", "1"); j != h {
		t.Fatalf("mode d'avant : devait rejoindre %d, obtenu %d", h, j)
	}
}

// TestAppariementVecteurBrut : la requete du jeu telle quelle passe par le gestionnaire.
func TestAppariementVecteurBrut(t *testing.T) {
	b := nouveauBanc(t, true)
	c1 := newTestConn(b.ep, 1, "198.51.100.1:1")
	c2 := newTestConn(b.ep, 2, "198.51.100.2:1")
	r1 := b.ext(c1, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, vecteurAppariement(t, "auto_1v1")))
	r2 := b.ext(c2, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, vecteurAppariement(t, "auto_1v1")))
	var s1, s2 MatchmakeSession
	NewStreamIn(r1.Body, b.s).Extract(&s1)
	NewStreamIn(r2.Body, b.s).Extract(&s2)
	if r1.IsError || r2.IsError || s1.ID == 0 || s1.ID != s2.ID || s2.NumParticipants != 2 {
		t.Fatalf("deux 1 contre 1 de meme tranche : %d et %d (n=%d)", s1.ID, s2.ID, s2.NumParticipants)
	}
}

// TestSonde3ACleDePartition : 0x6D.0x3A recoit le genre (u32, 4 octets, 0x16a76b8) et rend le
// u32 2 pour chacun : la valeur qui devient l'attribut 0 de la session et du critere (« 2 » dans
// les vecteurs du jeu), la meme pour tous les joueurs.
func TestSonde3ACleDePartition(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	c := newTestConn(ep, 1, "198.51.100.1:1")
	for _, genre := range []uint32{1, 2, 3, 7, 13} {
		o := NewStreamOut(s)
		o.U32(genre)
		rep := mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodSSBUPreMatch, 1, o.Bytes()))
		in := NewStreamIn(rep.Body, s)
		if v := in.U32(); rep.IsError || v != 2 || in.Remaining() != 0 {
			t.Fatalf("genre %d : %d (%d octets)", genre, v, len(rep.Body))
		}
	}
	if a := critereDuJeu(t, "critere_rapide_passe1").Attribs[0]; a != "2" {
		t.Fatalf("attribut 0 du critere : %q", a)
	}
}
