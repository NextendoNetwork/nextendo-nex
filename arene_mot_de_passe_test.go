package nex

import (
	"bytes"
	"testing"
)

// Arenes protegees par mot de passe (Super Smash Bros. Ultimate 13.0.5).
//
// Ce que le CODE DU JEU impose (main 13.0.5 reloge, adresses du binaire) :
//   - creation : le mot de passe part dans MatchmakeSession.UserPassword (Pia 0xe3480-0xe3554 le
//     copie en +0xf8, que le serialiseur 0x254b00 ecrit entre StartedTime et ReferGID). Rien
//     dans Pia n'ecrit UserPasswordEnabled (+0x124) hors des copies 0x254ad8 : le jeu l'envoie
//     a false, c'est au serveur de le poser ;
//   - liste : Pia recopie UserPasswordEnabled de chaque session recue (0xd91f0 -> 0x112be0) et
//     Smash le range dans l'entree de l'arene (0x3706c98) : c'est le cadenas ;
//   - jonction : JoinMatchmakeSessionWithParam (0x6D.0x27), mot de passe saisi dans le 6e champ
//     de JoinMatchmakeSessionParam (serialiseur 0x252b30, String en +0x38, posee par 0xe4904) ;
//   - refus : seul RendezVous 0x800300D5 (MatchmakeSessionUserPasswordUnmatch) donne la fenetre
//     « Le code d'acces est incorrect. » (table 0x449563c -> Pia 0x6c51 -> 2618-0545 ->
//     0x16a8608 -> -0x3d6 -> type 0x1e -> pop_online_room_not_enter_diff_password).
//
// PID fictifs 18009xxxxx.
const (
	pidHoteArene    uint64 = 1800900001
	pidBonMdp       uint64 = 1800900002
	pidMauvaisMdp   uint64 = 1800900003
	pidSansMdp      uint64 = 1800900004
	pidArenePublic  uint64 = 1800900005
	mdpArene               = "13572468"
	resultMdpRefuse uint32 = 0x800300D5 // RendezVous::MatchmakeSessionUserPasswordUnmatch, erreur
)

// corpsRejoindre reproduit champ pour champ le serialiseur du jeu 0x252b30.
func corpsRejoindre(s *Settings, gid uint32, mdp string) []byte {
	sub := NewStreamOut(s)
	sub.U32(gid)    // +0xc
	sub.U32(0)      // List<PID> participants (+0x10), vide
	sub.U32(0)      // gid_for_participation_check (+0x28)
	sub.U32(0)      // join_matchmake_session_option (+0x2c)
	sub.U8(0)       // join_matchmake_session_behavior (+0x30)
	sub.String(mdp) // mot de passe utilisateur (+0x38)
	sub.String("")  // mot de passe systeme (+0x60)
	sub.String("")  // message (+0x88)
	sub.U16(1)      // participation_count (+0xb0)
	sub.U16(0)      // extra_participants (+0xb2)
	bl := NewStreamOut(s)
	bl.U32(0)
	sub.U8(0) // MatchmakeBlockListParam (+0xb8) : en-tete + options
	sub.Buffer(bl.Bytes())
	out := NewStreamOut(s)
	out.U8(0)
	out.Buffer(sub.Bytes())
	return out.Bytes()
}

type bancArene struct {
	t   *testing.T
	s   *Settings
	ep  *Endpoint
	mm  *Matchmaking
	ext RMCHandler
	mmh RMCHandler
}

func nouveauBancArene(t *testing.T) *bancArene {
	s := testSettings()
	mm := NewMatchmaking()
	return &bancArene{t: t, s: s, ep: NewEndpoint(s), mm: mm, ext: mm.ExtensionHandler(), mmh: mm.MatchMakingHandler()}
}

// creer ouvre une arene comme le jeu : UserPassword renseigne, UserPasswordEnabled a false.
func (b *bancArene) creer(c *Connection, mode uint32, mdp string) MatchmakeSession {
	p := &AutoMatchmakeParam{Session: MatchmakeSession{
		Gathering:         Gathering{MaxParticipants: 8},
		GameMode:          mode,
		OpenParticipation: true,
		Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		UserPassword:      mdp,
	}}
	body := NewStreamOut(b.s)
	body.Add(p)
	resp := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodCreateMatchmakeSessionWithParam, 1, body.Bytes()))
	if resp == nil || resp.IsError {
		b.t.Fatalf("creation refusee : %+v", resp)
	}
	var out MatchmakeSession
	NewStreamIn(resp.Body, b.s).Extract(&out)
	return out
}

func (b *bancArene) rejoindre(c *Connection, gid uint32, mdp string) *RMCMessage {
	return b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodJoinMatchmakeSessionWithParam, 2, corpsRejoindre(b.s, gid, mdp)))
}

func (b *bancArene) participants(gid uint32) []uint64 {
	b.mm.mu.Lock()
	defer b.mm.mu.Unlock()
	return append([]uint64(nil), b.mm.gatherings[gid].participants...)
}

func (b *bancArene) parcourir(c *Connection, exclureMdp bool) []MatchmakeSession {
	crit := &MatchmakeSessionSearchCriteria{GameMode: "", ExcludeUserPassword: exclureMdp}
	body := NewStreamOut(b.s)
	body.Add(crit)
	resp := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 3, body.Bytes()))
	in := NewStreamIn(resp.Body, b.s)
	n := in.U32()
	out := make([]MatchmakeSession, n)
	for i := range out {
		in.Extract(&out[i])
	}
	if in.Err() != nil {
		b.t.Fatalf("liste illisible : %v", in.Err())
	}
	return out
}

func refusMdp(r *RMCMessage) bool { return r != nil && r.IsError && r.Result == resultMdpRefuse }

// TestAreneMdpJonction : bon mot de passe accepte, mauvais et vide refuses avec le code que
// le jeu traduit en « Le code d'acces est incorrect. », hote et participant deja present
// jamais re-verifies.
func TestAreneMdpJonction(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	arene := b.creer(hote, 0x51, mdpArene)
	gid := arene.ID

	b.mm.mu.Lock()
	drapeau := b.mm.gatherings[gid].session.UserPasswordEnabled
	b.mm.mu.Unlock()
	if !drapeau {
		t.Errorf("une arene creee avec un mot de passe doit etre marquee UserPasswordEnabled (le jeu ne le pose jamais lui-meme)")
	}

	mauvais := newTestConn(b.ep, pidMauvaisMdp, "192.0.2.3:1")
	if r := b.rejoindre(mauvais, gid, "13572469"); !refusMdp(r) {
		t.Errorf("mauvais mot de passe : attendu l'erreur %#x, recu erreur=%v result=%#x", resultMdpRefuse, r != nil && r.IsError, r.Result)
	}
	sans := newTestConn(b.ep, pidSansMdp, "192.0.2.4:1")
	if r := b.rejoindre(sans, gid, ""); !refusMdp(r) {
		t.Errorf("sans mot de passe : attendu l'erreur %#x, recu erreur=%v result=%#x", resultMdpRefuse, r != nil && r.IsError, r.Result)
	}
	if p := b.participants(gid); len(p) != 1 {
		t.Fatalf("un refus ne doit ajouter personne, participants=%v", p)
	}

	bon := newTestConn(b.ep, pidBonMdp, "192.0.2.2:1")
	r := b.rejoindre(bon, gid, mdpArene)
	if r == nil || r.IsError {
		t.Fatalf("bon mot de passe refuse : %+v", r)
	}
	var s MatchmakeSession
	NewStreamIn(r.Body, b.s).Extract(&s)
	if s.ID != gid || len(s.SessionKey) == 0 {
		t.Fatalf("jonction acceptee mais session inattendue : gid=%d cle=%d", s.ID, len(s.SessionKey))
	}

	// Deja presents : ni l'hote ni le joueur admis ne retapent le mot de passe.
	if r := b.rejoindre(hote, gid, ""); r == nil || r.IsError {
		t.Fatalf("l'hote ne doit pas etre bloque par son propre mot de passe : %+v", r)
	}
	if r := b.rejoindre(bon, gid, ""); r == nil || r.IsError {
		t.Fatalf("un participant deja present ne doit pas etre re-verifie : %+v", r)
	}
	if p := b.participants(gid); len(p) != 2 || p[0] != pidHoteArene || p[1] != pidBonMdp {
		t.Fatalf("participants=%v, attendu [hote, admis]", p)
	}
}

// TestAreneMdpHoteRevenu : le proprietaire sorti de la liste des participants revient sans
// mot de passe (meme regle que pour l'hote present).
func TestAreneMdpHoteRevenu(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	gid := b.creer(hote, 0x51, mdpArene).ID
	b.mm.mu.Lock()
	b.mm.gatherings[gid].participants = nil
	b.mm.mu.Unlock()
	if r := b.rejoindre(hote, gid, ""); r == nil || r.IsError {
		t.Fatalf("le proprietaire doit rentrer sans mot de passe : %+v", r)
	}
}

// TestAreneSansMdpInchangee : une arene ouverte se rejoint comme avant, quel que soit le
// mot de passe envoye (le jeu n'en envoie pas).
func TestAreneSansMdpInchangee(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	gid := b.creer(hote, 0x51, "").ID
	for i, mdp := range []string{"", "nimporte"} {
		c := newTestConn(b.ep, pidArenePublic+uint64(i)*10, "192.0.2.5:1")
		if r := b.rejoindre(c, gid, mdp); r == nil || r.IsError {
			t.Fatalf("arene sans mot de passe refusee (mdp=%q) : %+v", mdp, r)
		}
	}
	b.mm.mu.Lock()
	drapeau := b.mm.gatherings[gid].session.UserPasswordEnabled
	b.mm.mu.Unlock()
	if drapeau {
		t.Fatalf("une arene sans mot de passe ne doit pas porter le cadenas")
	}
}

// TestAreneMdpCorpsIlisible : une requete tronquee avant le mot de passe n'ouvre pas une
// arene protegee.
func TestAreneMdpCorpsIlisible(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	gid := b.creer(hote, 0x51, mdpArene).ID
	sub := NewStreamOut(b.s)
	sub.U32(gid)
	out := NewStreamOut(b.s)
	out.U8(0)
	out.Buffer(sub.Bytes())
	c := newTestConn(b.ep, pidSansMdp, "192.0.2.4:1")
	r := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodJoinMatchmakeSessionWithParam, 2, out.Bytes()))
	if r == nil || !r.IsError {
		t.Fatalf("corps tronque accepte sur une arene protegee")
	}
	if p := b.participants(gid); len(p) != 1 {
		t.Fatalf("participants=%v apres un corps tronque", p)
	}
}

// TestAreneMdpListe : l'arene protegee figure dans la liste avec son cadenas, sans mot de passe
// ni cle ; elle disparait si le joueur demande d'exclure les arenes a code d'acces.
func TestAreneMdpListe(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	gid := b.creer(hote, 0x51, mdpArene).ID
	ouverte := b.creer(newTestConn(b.ep, pidArenePublic, "192.0.2.5:1"), 0x51, "").ID
	vu := newTestConn(b.ep, pidSansMdp, "192.0.2.4:1")

	trouvee := false
	for _, s := range b.parcourir(vu, false) {
		if s.ID != gid {
			continue
		}
		trouvee = true
		if !s.UserPasswordEnabled {
			t.Errorf("arene protegee listee sans cadenas")
		}
		if s.UserPassword != "" || len(s.SessionKey) != 0 {
			t.Errorf("la liste divulgue mot de passe (%d o) ou cle (%d o)", len(s.UserPassword), len(s.SessionKey))
		}
	}
	if !trouvee {
		t.Fatalf("arene protegee absente de la liste alors que le joueur inclut les arenes a code d'acces")
	}
	for _, s := range b.parcourir(vu, true) {
		if s.ID == gid {
			t.Fatalf("arene protegee listee alors que le joueur les exclut")
		}
	}
	ok := false
	for _, s := range b.parcourir(vu, true) {
		ok = ok || s.ID == ouverte
	}
	if !ok {
		t.Fatalf("l'arene ouverte doit rester listee")
	}
}

// TestAreneMdpAucuneFuite : aucune reponse de recherche ne rend le mot de passe ni la cle
// d'une arene protegee (0x6D.0x30, 0x6D.0x31, 0x15.0x15), et ses stations ne sont livrees
// qu'a ses participants.
func TestAreneMdpAucuneFuite(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	st := NewStationURL("prudp")
	st.Set("address", "192.0.2.1")
	st.SetInt("RVCID", int(hote.ID))
	hote.SetStations([]*StationURL{st})
	gid := b.creer(hote, 0x51, mdpArene).ID
	SecureConnectionHandler()(hote, replaceURLReq(b.s, 9, st))
	curieux := newTestConn(b.ep, pidSansMdp, "192.0.2.4:1")

	verifie := func(quoi string, s MatchmakeSession) {
		t.Helper()
		if s.ID != gid {
			t.Fatalf("%s : gid %d au lieu de %d", quoi, s.ID, gid)
		}
		if s.UserPassword != "" || len(s.SessionKey) != 0 {
			t.Errorf("%s divulgue le mot de passe (%d o) ou la cle (%d o)", quoi, len(s.UserPassword), len(s.SessionKey))
		}
		if !s.UserPasswordEnabled {
			t.Errorf("%s : cadenas absent", quoi)
		}
	}

	liste := NewStreamOut(b.s)
	liste.U32(1)
	liste.U32(gid)
	r := b.ext(curieux, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodFindByGidList, 4, liste.Bytes()))
	in := NewStreamIn(r.Body, b.s)
	if in.U32() != 1 {
		t.Fatalf("0x30 : une session attendue")
	}
	var s30 MatchmakeSession
	in.Extract(&s30)
	verifie("0x6D.0x30", s30)

	un := NewStreamOut(b.s)
	un.U32(gid)
	var s31 MatchmakeSession
	NewStreamIn(b.ext(curieux, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodFindMatchmakeSessionBySingleID, 5, un.Bytes())).Body, b.s).Extract(&s31)
	verifie("0x6D.0x31", s31)

	in15 := NewStreamIn(b.mmh(curieux, NewRMCRequest(b.s, ProtocolMatchMaking, MethodFindBySingleID15, 6, un.Bytes())).Body, b.s)
	if !in15.Bool() {
		t.Fatalf("0x15.0x15 : session attendue")
	}
	var s15 MatchmakeSession
	in15.Extract(&s15)
	verifie("0x15.0x15", s15)

	// Stations : rien pour qui n'est pas entre, la station de l'hote pour un admis.
	urls := func(c *Connection) []*StationURL {
		r := b.mmh(c, NewRMCRequest(b.s, ProtocolMatchMaking, MethodGetSessionURLs, 7, un.Bytes()))
		return ReadList(NewStreamIn(r.Body, b.s), func(i *StreamIn) *StationURL { return i.StationURLValue() })
	}
	if u := urls(curieux); len(u) != 0 {
		t.Errorf("stations de l'hote d'une arene protegee livrees a un non-participant : %d", len(u))
	}
	admis := newTestConn(b.ep, pidBonMdp, "192.0.2.2:1")
	if r := b.rejoindre(admis, gid, mdpArene); r == nil || r.IsError {
		t.Fatalf("jonction admise refusee : %+v", r)
	}
	if u := urls(admis); len(u) != 1 {
		t.Errorf("un participant admis doit recevoir la station de l'hote, recu %d", len(u))
	}
}

// TestAreneMdpPartieRapide : AutoMatchmake n'ouvre une arene protegee qu'au porteur du bon
// mot de passe. Le jeu n'envoie jamais UserPasswordEnabled : seul le mot de passe compte.
func TestAreneMdpPartieRapide(t *testing.T) {
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	gid := b.creer(hote, 0x52, mdpArene).ID

	// OpenParticipation a false : la session qu'ouvre un essai sans succes n'est jamais
	// candidate ensuite (avant : ouverte, le porteur du bon mot de passe y entrait une fois
	// sur quelques-unes, selon l'ordre du parcours de la table, et le test echouait).
	auto := func(c *Connection, mdp string) MatchmakeSession {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering:         Gathering{MaxParticipants: 8},
			GameMode:          0x52,
			OpenParticipation: false,
			Attribs:           []uint32{0, 0, 0, 0, 0, 0},
			UserPassword:      mdp,
		}}
		body := NewStreamOut(b.s)
		body.Add(p)
		var out MatchmakeSession
		NewStreamIn(b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 8, body.Bytes())).Body, b.s).Extract(&out)
		return out
	}
	// Le bon mot de passe d'abord, tant que l'arene est le seul salon de ce mode : apres les
	// deux refus, un salon ouvert (sans mot de passe) cree par le premier refuse convenait
	// AUSSI au porteur du bon mot de passe, et le choix entre les deux suivait l'ordre de
	// parcours de la carte (echec d'environ une fois sur dix). Cet ordre (relais-strict-amis)
	// et OpenParticipation a false (smash) sont gardes tous les deux : chacun suffit.
	if s := auto(newTestConn(b.ep, pidBonMdp, "192.0.2.2:1"), mdpArene); s.ID != gid {
		t.Fatalf("AutoMatchmake avec le bon mot de passe devait rejoindre l'arene %d, recu %d", gid, s.ID)
	}
	if s := auto(newTestConn(b.ep, pidSansMdp, "192.0.2.4:1"), ""); s.ID == gid {
		t.Fatalf("AutoMatchmake sans mot de passe est entre dans l'arene protegee")
	}
	if s := auto(newTestConn(b.ep, pidMauvaisMdp, "192.0.2.3:1"), "00000000"); s.ID == gid {
		t.Fatalf("AutoMatchmake avec un mauvais mot de passe est entre dans l'arene protegee")
	}
}

// TestCorpsRejoindreCommeLeJeu fige l'encodage de reference : 6e champ = mot de passe.
func TestCorpsRejoindreCommeLeJeu(t *testing.T) {
	s := testSettings()
	c := corpsRejoindre(s, 7, "12")
	// u8 version, u32 taille, u32 gid, u32 n, u32, u32, u8, puis String « 12 ».
	want := []byte{0x03, 0x00, '1', '2', 0x00}
	if !bytes.Equal(c[1+4+4+4+4+4+1:1+4+4+4+4+4+1+5], want) {
		t.Fatalf("mot de passe mal place : % x", c)
	}
}
