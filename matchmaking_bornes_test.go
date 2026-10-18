package nex

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Bornes du matchmaking fidele (relecture du chantier b-matchmaking) : memoire de 0x6D.0x2C,
// verrou de 0x6D.0x28 / 0x6D.0x34, taille des journaux, lecture stricte.

// majGrosse : requete 0x6D.0x2C du proprietaire avec n cles de parametres inconnues de taille t,
// natt attributs, et les cinq cles du jeu.
func majGrosse(s *Settings, gid uint32, n, t, natt int, graine int) []byte {
	p := UpdateMatchmakeSessionParam{GID: gid, ModificationFlags: 0x3841, MinParticipants: 2, MaxParticipants: 2,
		MatchmakeSystem: 1, ParticipationPolicy: 95, Param: MatchmakeParam{Params: map[string]Variant{
			"@CC": {Type: VariantString, String: "FR"}, "@RV": {Type: VariantUint64, Uint: 1500},
			"@DR": {Type: VariantUint64}, "@VR": {Type: VariantUint64}, "_RU": {Type: VariantUint64, Uint: 0x10003},
		}}}
	p.Attributes = make([]uint32, natt)
	for i := range p.Attributes {
		p.Attributes[i] = uint32(i)
	}
	remplissage := strings.Repeat("x", t)
	for i := 0; i < n; i++ {
		p.Param.Params[fmt.Sprintf("k%d-%d", graine, i)] = Variant{Type: VariantString, String: remplissage}
	}
	o := NewStreamOut(s)
	o.Add(&p)
	return o.Bytes()
}

// TestMajSessionNeGrossitPas : des mises a jour de plusieurs centaines de Ko, chacune avec des
// cles nouvelles, ne font pas grossir la session : seules les cinq cles du jeu (0x3708c70) sont
// ecrites, un attribut de trop annule l'ecriture des attributs, et la reponse 0x34 rendue a un
// autre joueur reste petite. Avant : chaque cle s'ajoutait (40 x 0,8 Mo = 112 Mo de tas, 34 Mo
// de reponse 0x34).
func TestMajSessionNeGrossitPas(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.SessionPartSmash = func(*Connection) bool { return true }
	hote := newTestConn(ep, 30, "198.51.100.30:1")
	gid := sessionPourMaj(t, mm, hote, 7)
	mm.mu.Lock()
	clesAvant := len(mm.gatherings[gid].session.Param.Params)
	mm.mu.Unlock()
	for i := 0; i < 12; i++ {
		corps := majGrosse(s, gid, 2000, 100, 6, i)
		if len(corps) < 200000 {
			t.Fatalf("requete de %d octets seulement", len(corps))
		}
		rep := mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 2, corps))
		if rep.IsError {
			t.Fatal("reponse en erreur")
		}
	}
	// 70 000 attributs : ecriture des attributs ignoree.
	mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 3, majGrosse(s, gid, 0, 0, 70000, 99)))
	mm.mu.Lock()
	ss := mm.gatherings[gid].session
	n, na := len(ss.Param.Params), len(ss.Attribs)
	ru := ss.Param.Params["_RU"].Uint
	mm.mu.Unlock()
	if n > clesAvant+5 || na != 6 || ru != 0x10003 {
		t.Fatalf("session : %d cles (%d a la creation), %d attributs, _RU=%#x", n, clesAvant, na, ru)
	}
	rep := mm.ExtensionHandler()(newTestConn(ep, 31, "198.51.100.31:1"), NewRMCRequest(s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 4, vecteurAppariement(t, "critere_arene_sans_filtre")))
	if len(rep.Body) > 4096 {
		t.Fatalf("reponse 0x34 de %d octets", len(rep.Body))
	}
}

// TestMajSessionChaineLongue : une cle du jeu dont la chaine depasse 16 octets n'est pas ecrite.
func TestMajSessionChaineLongue(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.SessionPartSmash = func(*Connection) bool { return true }
	hote := newTestConn(ep, 30, "198.51.100.30:1")
	gid := sessionPourMaj(t, mm, hote, 1)
	p := UpdateMatchmakeSessionParam{GID: gid, ModificationFlags: majParam, Param: MatchmakeParam{Params: map[string]Variant{
		"@CC": {Type: VariantString, String: strings.Repeat("A", 17)}, "@RV": {Type: VariantUint64, Uint: 1600}}}}
	o := NewStreamOut(s)
	o.Add(&p)
	mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 2, o.Bytes()))
	mm.mu.Lock()
	defer mm.mu.Unlock()
	pp := mm.gatherings[gid].session.Param.Params
	if _, ok := pp["@CC"]; ok || pp["@RV"].Uint != 1600 {
		t.Fatalf("parametres : %s", texteParam(pp))
	}
}

// TestMajSessionTronquee : drapeau 0x2000 et contenu coupe A L'INTERIEUR de la Structure (la
// longueur annoncee suit la coupe) : la lecture tolerante rendait MatchmakeSystem 0 sans erreur
// et la session sortait de l'appariement ; la lecture stricte refuse, rien n'est applique.
func TestMajSessionTronquee(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.SessionPartSmash = func(*Connection) bool { return true }
	hote := newTestConn(ep, 30, "198.51.100.30:1")
	gid := sessionPourMaj(t, mm, hote, 1)
	p := UpdateMatchmakeSessionParam{GID: gid, ModificationFlags: majSysteme | majOuverte, OpenParticipation: true,
		MatchmakeSystem: 1, ParticipationPolicy: 95}
	o := NewStreamOut(s)
	o.Add(&p)
	b := o.Bytes()
	// en-tete : u8 version, u32 longueur ; on retire les 24 derniers octets du contenu.
	coupe := append([]byte(nil), b[:len(b)-24]...)
	binary.LittleEndian.PutUint32(coupe[1:], uint32(len(coupe)-5))
	var tol UpdateMatchmakeSessionParam
	in := NewStreamIn(coupe, s)
	in.Extract(&tol)
	if in.Err() != nil || tol.MatchmakeSystem != 0 {
		t.Fatalf("precondition : lecture tolerante err=%v sys=%d", in.Err(), tol.MatchmakeSystem)
	}
	if _, lu := lireMajSession(coupe, s); lu {
		t.Fatal("lecture stricte : contenu tronque accepte")
	}
	mm.mu.Lock()
	mm.gatherings[gid].session.OpenParticipation = false
	mm.mu.Unlock()
	mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 2, coupe))
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if ss := mm.gatherings[gid].session; ss.MatchmakeSystem != 1 || ss.OpenParticipation {
		t.Fatalf("session modifiee par une requete tronquee : sys=%d ouverte=%v", ss.MatchmakeSystem, ss.OpenParticipation)
	}
	// Les vecteurs du jeu restent lus en lecture stricte.
	for _, nom := range []string{"maj_1v1", "maj_smash4_ouverte"} {
		if _, lu := lireMajSession(vecteurAppariement(t, nom), s); !lu {
			t.Fatalf("%s refuse en lecture stricte", nom)
		}
	}
}

// TestMajSessionHorsDuSalon : le proprietaire sorti de son salon (0x32.0x1, OwnerPID inchange)
// ne le modifie plus.
func TestMajSessionHorsDuSalon(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.SessionPartSmash = func(*Connection) bool { return true }
	hote := newTestConn(ep, 30, "198.51.100.30:1")
	gid := sessionPourMaj(t, mm, hote, 1)
	mm.mu.Lock()
	g := mm.gatherings[gid]
	g.participants = append(g.participants, 31)
	g.session.OpenParticipation = false
	mm.mu.Unlock()
	o := NewStreamOut(s)
	o.U32(gid)
	mm.MatchMakingExtHandler()(hote, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 2, o.Bytes()))
	mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 3, majDuJeu(t, "maj_smash4_ouverte", gid)))
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if g.session.OpenParticipation || g.session.MaxParticipants != 2 {
		t.Fatalf("modifie par un proprietaire sorti : %+v", g.session)
	}
}

// criteresGonfles : requete 0x6D.0x28 du vecteur nom dont les criteres sont modifies par f.
func criteresGonfles(t *testing.T, s *Settings, nom string, f func(p *AutoMatchmakeParamComplet)) []byte {
	t.Helper()
	p, ok := lireAutoMatchmakeComplet(vecteurAppariement(t, nom), s)
	if !ok {
		t.Fatal(nom)
	}
	f(&p)
	o := NewStreamOut(s)
	o.Add(&p)
	return o.Bytes()
}

// TestAppariementCriteresHorsBornes : 5 criteres, 7 attributs ou un champ de 33 octets : les
// criteres ne sont pas evalues (comportement d'avant : le joueur de Smash a 4 entre aussitot
// dans le salon 1 contre 1) ; dans les bornes, il est refuse (palier 2 pas encore permis, en
// refus strict RefusDes pour que le palier se voie).
func TestAppariementCriteresHorsBornes(t *testing.T) {
	for _, cas := range []struct {
		nom     string
		f       func(p *AutoMatchmakeParamComplet)
		rejoint bool
	}{
		{"jeu", func(*AutoMatchmakeParamComplet) {}, false},
		{"5 criteres", func(p *AutoMatchmakeParamComplet) {
			for len(p.Criteres) < 5 {
				p.Criteres = append(p.Criteres, p.Criteres[0])
			}
		}, true},
		{"7 attributs", func(p *AutoMatchmakeParamComplet) { p.Criteres[0].Attribs = append(p.Criteres[0].Attribs, "") }, true},
		{"champ de 33 octets", func(p *AutoMatchmakeParamComplet) { p.Criteres[0].Attribs[3] = strings.Repeat("1", 33) }, true},
		{"mode de 33 octets", func(p *AutoMatchmakeParamComplet) { p.Criteres[0].GameMode = strings.Repeat(" ", 32) + "1" }, true},
	} {
		b := nouveauBanc(t, true)
		b.pal.RefusDes = 1
		g1 := b.chercher(1, "auto_1v1", 11, "10,12", "0")
		c := newTestConn(b.ep, 2, "198.51.100.2:1")
		resp := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, criteresGonfles(t, b.s, "auto_smash4", cas.f)))
		var out MatchmakeSession
		NewStreamIn(resp.Body, b.s).Extract(&out)
		if (out.ID == g1) != cas.rejoint {
			t.Fatalf("%s : gid=%d (salon 1 contre 1 %d), rejoint attendu=%v", cas.nom, out.ID, g1, cas.rejoint)
		}
	}
}

// TestAppariementGrosCritereVerrou : 150 salons, un critere de 330 000 attributs (~1 Mo) : la
// requete ne tient plus le verrou (les criteres ne sont pas evalues), elle rend la main vite.
// Avant : environ 200 ms sous m.mu par requete.
func TestAppariementGrosCritereVerrou(t *testing.T) {
	b := nouveauBanc(t, true)
	for i := 0; i < 150; i++ {
		b.chercher(uint64(1000+i), "auto_1v1", uint32(i), fmt.Sprintf("%d,%d", i, i), "1") // 150 tranches : 150 salons ouverts
	}
	corps := criteresGonfles(t, b.s, "auto_smash4", func(p *AutoMatchmakeParamComplet) {
		a := make([]string, 330000)
		for i := range a {
			a[i] = "" // champ vide = tout : chaque salon parcourt les 330 000 attributs
		}
		p.Criteres[0].Attribs = a
	})
	c := newTestConn(b.ep, 2, "198.51.100.2:1")
	pire := attenteVerrouMax(b.mm, func() {
		b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, corps))
	})
	t.Logf("verrou tenu au plus %v", pire)
	if pire > 50*time.Millisecond {
		t.Fatalf("verrou tenu %v", pire)
	}
}

// attenteVerrouMax : la plus longue attente pour prendre m.mu pendant que f tourne.
func attenteVerrouMax(mm *Matchmaking, f func()) time.Duration {
	fini := make(chan struct{})
	go func() {
		f()
		close(fini)
	}()
	var pire time.Duration
	for enCours := true; enCours; {
		select {
		case <-fini:
			enCours = false
		default:
		}
		t0 := time.Now()
		mm.mu.Lock()
		mm.mu.Unlock()
		if d := time.Since(t0); d > pire {
			pire = d
		}
	}
	return pire
}

// TestParcoursGrosCritereVerrou : 150 arenes, recherche 0x34 filtre actif avec un critere de
// 330 000 attributs vides : hors bornes, il n'est pas evalue sous m.mu (avant : environ 200 ms).
func TestParcoursGrosCritereVerrou(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.FiltreParcours = func(*Connection) bool { return true }
	for i := 0; i < 150; i++ {
		c := newTestConn(ep, uint64(100+i), "198.51.100.1:1")
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, GameMode: 7,
			OpenParticipation: true, MatchmakeSystem: 1, Attribs: []uint32{2, 1, 0, 2, 0, 0}}}
		o := NewStreamOut(s)
		o.Add(p)
		mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCreateMatchmakeSessionWithParam, 1, o.Bytes()))
	}
	cr := critereDuJeu(t, "critere_arene_sans_filtre")
	cr.Attribs = make([]string, 330000)
	q := NewStreamOut(s)
	q.Add(&cr)
	corps := q.Bytes()
	var n uint32
	pire := attenteVerrouMax(mm, func() {
		rep := mm.ExtensionHandler()(newTestConn(ep, 9, "198.51.100.9:1"), NewRMCRequest(s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 2, corps))
		n = NewStreamIn(rep.Body, s).U32()
	})
	t.Logf("verrou tenu au plus %v, %d arene(s)", pire, n)
	if pire > 50*time.Millisecond || n != 150 {
		t.Fatalf("verrou tenu %v, %d arene(s)", pire, n)
	}
}

// TestParcoursCritereHorsBornes : un critere de 0x34 hors bornes n'est pas applique (toute
// session ouverte, comme filtre coupe).
func TestParcoursCritereHorsBornes(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.FiltreParcours = func(*Connection) bool { return true }
	for i, mode := range []uint32{7, 7, 1} {
		c := newTestConn(ep, uint64(1+i), "198.51.100.1:1")
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, GameMode: mode,
			OpenParticipation: true, MatchmakeSystem: 1, Attribs: []uint32{2, uint32(1 + 2*i), 0, 2, 0, 0}}}
		o := NewStreamOut(s)
		o.Add(p)
		mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCreateMatchmakeSessionWithParam, 1, o.Bytes()))
	}
	parcourir := func(cr MatchmakeSessionSearchCriteria) uint32 {
		q := NewStreamOut(s)
		q.Add(&cr)
		rep := mm.ExtensionHandler()(newTestConn(ep, 9, "198.51.100.9:1"), NewRMCRequest(s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 2, q.Bytes()))
		return NewStreamIn(rep.Body, s).U32()
	}
	cr := critereDuJeu(t, "critere_arene_filtres")
	if n := parcourir(cr); n != 1 {
		t.Fatalf("dans les bornes : %d session(s)", n)
	}
	cr.Attribs = append(cr.Attribs, "")
	if n := parcourir(cr); n != 3 {
		t.Fatalf("7 attributs : %d session(s), 3 attendues (liste d'avant)", n)
	}
}

// TestParcoursArenesAmisSeulement : l'arene « amis seulement » est de genre 8 (creation
// 0x189d7e4-0x189d800 : genre 7, plus 1 quand le reglage « qui peut entrer » 0x53069b1 vaut 2).
// La recherche publique (genre 7, vecteur critere_arene_filtres) ne la rend pas, qu'elle porte
// l'attribut 0 que le jeu lui donne (0, masque 0x20ae) ou meme celui d'une arene ouverte ; un
// salon de Partie rapide non plus. Elle reste dans la liste des arenes d'amis (0x6D.0x33).
func TestParcoursArenesAmisSeulement(t *testing.T) {
	b := nouveauBancSmash(t)
	b.mm.FiltreParcours = func(*Connection) bool { return true }
	creer := func(pid uint64, mode uint32, attribs []uint32, meth uint32) uint32 {
		c := newTestConn(b.ep, pid, "198.51.100.1:1")
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, GameMode: mode,
			OpenParticipation: true, MatchmakeSystem: 1, Attribs: attribs}}
		o := NewStreamOut(b.s)
		o.Add(p)
		r := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, meth, 1, o.Bytes()))
		var ss MatchmakeSession
		NewStreamIn(r.Body, b.s).Extract(&ss)
		if r.IsError || ss.ID == 0 {
			t.Fatalf("creation genre %d : %+v", mode, r)
		}
		return ss.ID
	}
	ouverte := creer(1800900301, 7, []uint32{2, 1, 0, 2, 0, 0}, MethodCreateMatchmakeSessionWithParam)
	amisJeu := creer(1800900302, 8, []uint32{0, 1, 0, 2, 0, 0}, MethodCreateMatchmakeSessionWithParam)
	amisAttr := creer(1800900303, 8, []uint32{2, 1, 0, 2, 0, 0}, MethodCreateMatchmakeSessionWithParam)
	creer(1800900304, 1, []uint32{2, 1, 0, 2, 0, 0}, MethodAutoMatchmakeWithParamPostpone)
	demandeur := newTestConn(b.ep, 1800900309, "198.51.100.9:1")
	rep := b.ext(demandeur, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 2, vecteurAppariement(t, "critere_arene_filtres")))
	in := NewStreamIn(rep.Body, b.s)
	n := int(in.U32())
	var vus []uint32
	for i := 0; i < n; i++ {
		var ss MatchmakeSession
		in.Extract(&ss)
		vus = append(vus, ss.ID)
	}
	if rep.IsError || n != 1 || vus[0] != ouverte {
		t.Fatalf("recherche publique : %v, seule l'arene ouverte %d attendue", vus, ouverte)
	}
	amis := b.arenesAmis(demandeur, 1800900302, 1800900303)
	trouves := map[uint32]bool{}
	for _, r := range amis {
		trouves[r.Session.ID] = true
	}
	if len(amis) != 2 || !trouves[amisJeu] || !trouves[amisAttr] {
		t.Fatalf("arenes d'amis : %d resultat(s) %v, attendu %d et %d", len(amis), trouves, amisJeu, amisAttr)
	}
	// Raison du refus pour le journal : le genre.
	c := critereDuJeu(t, "critere_arene_filtres")
	if r := critereRefus(&c, &MatchmakeSession{GameMode: 8, Attribs: []uint32{2, 1, 0, 2, 0, 0}, Gathering: Gathering{MaxParticipants: 4}, OpenParticipation: true, MatchmakeSystem: 1}, 1, -1); r != "mode" {
		t.Fatalf("refus d'une arene de genre 8 : %q", r)
	}
	// Partie rapide : un critere de genre 1 ne prend pas une arene.
	c = critereDuJeu(t, "critere_rapide_passe1")
	if critereAccepte(&c, &MatchmakeSession{GameMode: 7, Attribs: []uint32{2, 11, 0}, Gathering: Gathering{MaxParticipants: 2}, OpenParticipation: true, MatchmakeSystem: 1}, 1, -1) {
		t.Fatal("critere de genre 1 accepte une arene")
	}
}

// TestJournauxBornes : quoi que le client envoie, une ligne de journal reste courte.
func TestJournauxBornes(t *testing.T) {
	long := strings.Repeat("9", 100000)
	attribs := make([]string, 50000)
	for i := range attribs {
		attribs[i] = long[:20]
	}
	cs := make([]MatchmakeSessionSearchCriteria, 3000)
	for i := range cs {
		cs[i] = MatchmakeSessionSearchCriteria{Attribs: attribs, GameMode: long, Codeword: long}
	}
	params := map[string]Variant{}
	for i := 0; i < 20000; i++ {
		params[fmt.Sprintf("k%d", i)] = Variant{Type: VariantString, String: long}
	}
	cs[0].Param.Params = params
	cs[1].Param.Params = map[string]Variant{long: {Type: VariantString, String: long}}
	if n := len(texteCriteres(cs)); n > 8192 {
		t.Fatalf("texteCriteres : %d octets", n)
	}
	if n := len(texteParam(params)); n > 64 {
		t.Fatalf("texteParam : %d octets", n)
	}
	if n := len(texteAttribs(make([]uint32, 300000))); n > 128 {
		t.Fatalf("texteAttribs : %d octets", n)
	}
	// Ligne « applique » de 0x6D.0x2C : attributs de la session (venus de la creation) bornes.
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	hote := newTestConn(ep, 30, "198.51.100.30:1")
	gid := sessionPourMaj(t, mm, hote, 1)
	mm.mu.Lock()
	mm.gatherings[gid].session.Attribs = make([]uint32, 300000)
	mm.mu.Unlock()
	p := UpdateMatchmakeSessionParam{GID: gid, ModificationFlags: majOuverte, OpenParticipation: true}
	if fait := mm.appliquerMajSmash(hote, &p); len(fait) > 512 {
		t.Fatalf("ligne applique : %d octets", len(fait))
	}
	// Ligne « maj 0x2c » des PID de test : attributs, mot de code et parametres du client bornes.
	grosse := UpdateMatchmakeSessionParam{GID: gid, ModificationFlags: 0x3843, Attributes: make([]uint32, 300000),
		Codeword: long, UserPassword: long}
	grosse.Param.Params = params
	if n := len(texteMajSession(30, &grosse, true)); n > 1024 {
		t.Fatalf("ligne maj 0x2c : %d octets", n)
	}
	// Lignes de tous les joueurs : criteres resumes et raisons des refus bornes.
	if n := len(texteCriteresCourts(cs)); n > 4096 {
		t.Fatalf("texteCriteresCourts : %d octets", n)
	}
	toutes := map[string]int{"mode": 1 << 30, "attribut": 1, "min": 1, "max": 1, "systeme": 1, "places": 1, "verrou": 1, "mdp": 1, "code": 1}
	for _, a := range nomsAttributs {
		toutes[a] = 1 << 30
	}
	if n := len(texteRaisons(toutes)); n > 512 {
		t.Fatalf("texteRaisons : %d octets", n)
	}
}

// TestSerieCloseArrivantSansPaliers : un PID qui a les paliers, rejoint par un joueur qui ne les
// a pas, voit sa serie de recherches close (sinon sa recherche suivante aurait tout de suite
// droit au palier 3).
func TestSerieCloseArrivantSansPaliers(t *testing.T) {
	b := nouveauBanc(t, false)
	b.mm.Appariement = func(c *Connection) *PaliersAppariement {
		if c.PID == 1 {
			return b.pal
		}
		return nil
	}
	gid := b.chercher(1, "auto_1v1", 11, "10,12", "0")
	b.mm.mu.Lock()
	_, avant := b.mm.series[1]
	b.mm.mu.Unlock()
	if !avant {
		t.Fatal("serie du PID 1 non commencee")
	}
	if g := b.chercher(2, "auto_1v1", 11, "10,12", "0"); g != gid {
		t.Fatalf("le PID 2 devait rejoindre %d, obtenu %d", gid, g)
	}
	b.mm.mu.Lock()
	defer b.mm.mu.Unlock()
	if _, reste := b.mm.series[1]; reste {
		t.Fatal("serie du PID 1 gardee")
	}
}

// TestUnregisterSalonOrphelin : le proprietaire parti d'un salon orphelin, revenu, ne le supprime
// pas ; une fois migre, le nouveau proprietaire le peut.
func TestUnregisterSalonOrphelin(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.MigrationHote = func(uint64) bool { return true }
	ext := mm.ExtensionHandler()
	auto := func(conn *Connection) uint32 {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, GameMode: 1, OpenParticipation: true}}
		o := NewStreamOut(s)
		o.Add(p)
		var r MatchmakeSession
		NewStreamIn(ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
		return r.ID
	}
	a := newTestConn(ep, 50, "198.51.100.50:1")
	b := newTestConn(ep, 51, "198.51.100.51:1")
	gid := auto(a)
	auto(b)
	ep.unregisterConnection(a)
	mm.RemovePlayer(50)
	revenu := newTestConn(ep, 50, "198.51.100.50:2")
	o := NewStreamOut(s)
	o.U32(gid)
	mm.MatchMakingHandler()(revenu, NewRMCRequest(s, ProtocolMatchMaking, MethodUnregisterGathering, 2, o.Bytes()))
	mm.mu.Lock()
	_, reste := mm.gatherings[gid]
	mm.mu.Unlock()
	if !reste {
		t.Fatal("salon orphelin supprime par son ancien proprietaire")
	}
	mm.MatchMakingHandler()(b, NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 3, corpsUpdateSessionHost(gid, true)))
	mm.MatchMakingHandler()(b, NewRMCRequest(s, ProtocolMatchMaking, MethodUnregisterGathering, 4, o.Bytes()))
	mm.mu.Lock()
	_, reste = mm.gatherings[gid]
	mm.mu.Unlock()
	if reste {
		t.Fatal("le nouveau proprietaire ne peut pas supprimer son salon")
	}
}

// TestMigrationHoteSessionIsolee : dans un match de tournoi (ModeIsole), UpdateSessionHost est
// acquitte sans effet ni avis, comme avant.
func TestMigrationHoteSessionIsolee(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.Endpoint = ep
	mm.MigrationHote = func(uint64) bool { return true }
	ext := mm.ExtensionHandler()
	auto := func(conn *Connection) uint32 {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 2}, GameMode: 0x54000001, OpenParticipation: true}}
		o := NewStreamOut(s)
		o.Add(p)
		var r MatchmakeSession
		NewStreamIn(ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
		return r.ID
	}
	hote := newTestConn(ep, 60, "198.51.100.60:1")
	suiv, capSuiv := connCapte(ep, 61, "198.51.100.61:1")
	gid := auto(hote)
	if auto(suiv) != gid {
		t.Fatal("le second joueur doit rejoindre le match")
	}
	avant := len(capSuiv.rmc(t, s))
	rep := mm.MatchMakingHandler()(suiv, NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 2, corpsUpdateSessionHost(gid, true)))
	if rep.IsError {
		t.Fatal("reponse en erreur")
	}
	mm.mu.Lock()
	h, o := mm.gatherings[gid].session.HostPID, mm.gatherings[gid].session.OwnerPID
	mm.mu.Unlock()
	if h != 60 || o != 60 {
		t.Fatalf("match de tournoi : hote=%d proprietaire=%d", h, o)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(capSuiv.rmc(t, s)); n != avant {
		t.Fatalf("%d message(s) recu(s) apres UpdateSessionHost", n-avant)
	}
}

// TestLectureStricte : une erreur dans une Structure remonte au flux en lecture stricte, pas en
// lecture tolerante (inchangee pour le reste du coeur).
func TestLectureStricte(t *testing.T) {
	s := testSettings()
	o := NewStreamOut(s)
	o.Add(&MatchmakeBlockListParam{Options: 7})
	b := o.Bytes()
	coupe := append([]byte(nil), b[:len(b)-2]...)
	binary.LittleEndian.PutUint32(coupe[1:], uint32(len(coupe)-5))
	var p MatchmakeBlockListParam
	tol := NewStreamIn(coupe, s)
	tol.Extract(&p)
	if tol.Err() != nil {
		t.Fatalf("lecture tolerante : %v", tol.Err())
	}
	st := newStreamInStrict(coupe, s)
	st.Extract(&p)
	if st.Err() == nil {
		t.Fatal("lecture stricte : erreur perdue")
	}
	ok := newStreamInStrict(b, s)
	ok.Extract(&p)
	if ok.Err() != nil || p.Options != 7 {
		t.Fatalf("lecture stricte d'un contenu entier : %v %d", ok.Err(), p.Options)
	}
}
