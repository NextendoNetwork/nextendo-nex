package nex

import (
	"encoding/hex"
	"os"
	"testing"
)

// Arenes d amis de Smash (0x6D.0x33). PID fictifs 1800900xxx, NSA fictifs 18100900000000000xx,
// adresses de documentation.

// corpsParParticipant reproduit le serialiseur du jeu 0x252870 : en-tete de structure, u32
// nombre, u64 par identifiant, u32 options, MatchmakeBlockListParam (en-tete + u32, 0x252cf0).
func corpsParParticipant(s *Settings, ids ...uint64) []byte {
	sub := NewStreamOut(s)
	sub.U32(uint32(len(ids)))
	for _, id := range ids {
		sub.U64(id)
	}
	sub.U32(0) // options (+0x28)
	bl := NewStreamOut(s)
	bl.U32(0)
	sub.U8(0)
	sub.Buffer(bl.Bytes())
	out := NewStreamOut(s)
	out.U8(0)
	out.Buffer(sub.Bytes())
	return out.Bytes()
}

func nouveauBancSmash(t *testing.T) *bancArene {
	s := NewSwitchSettings(accessKeySSBU, 40000)
	mm := NewMatchmaking()
	return &bancArene{t: t, s: s, ep: NewEndpoint(s), mm: mm, ext: mm.ExtensionHandler(), mmh: mm.MatchMakingHandler()}
}

func (b *bancArene) arenesAmis(c *Connection, ids ...uint64) []FindMatchmakeSessionByParticipantResult {
	resp := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodFindByParticipant, 9, corpsParParticipant(b.s, ids...)))
	if resp == nil || resp.IsError {
		b.t.Fatalf("0x33 en erreur : %+v", resp)
	}
	in := NewStreamIn(resp.Body, b.s)
	n := in.U32()
	out := make([]FindMatchmakeSessionByParticipantResult, n)
	for i := range out {
		in.Extract(&out[i])
	}
	if in.Err() != nil || in.Remaining() != 0 {
		b.t.Fatalf("reponse illisible : err=%v reste=%d", in.Err(), in.Remaining())
	}
	return out
}

func TestParamParParticipantFormatDuJeu(t *testing.T) {
	s := NewSwitchSettings(accessKeySSBU, 40000)
	var p FindMatchmakeSessionByParticipantParam
	in := NewStreamIn(corpsParParticipant(s, 1800900201, 1810090000000000001), s)
	in.Extract(&p)
	if in.Err() != nil || in.Remaining() != 0 || len(p.PrincipalIDs) != 2 || p.PrincipalIDs[1] != 1810090000000000001 {
		t.Fatalf("lecture du corps du jeu : err=%v reste=%d ids=%v", in.Err(), in.Remaining(), p.PrincipalIDs)
	}
}

// TestArenesAmisSmash : l arene d un ami sort, avec cadenas mais sans cle ni mot de passe ;
// un salon de Partie rapide, une arene fermee, un match de tournoi isole ou au code « T- »
// ne sortent pas ; un joueur sans arene ne produit rien.
func TestArenesAmisSmash(t *testing.T) {
	b := nouveauBancSmash(t)
	hote := newTestConn(b.ep, 1800900201, "192.0.2.1:1")
	invite := newTestConn(b.ep, 1800900202, "192.0.2.2:1")
	protege := newTestConn(b.ep, 1800900203, "192.0.2.3:1")
	rapide := newTestConn(b.ep, 1800900204, "192.0.2.4:1")
	ferme := newTestConn(b.ep, 1800900205, "192.0.2.5:1")
	tournoi := newTestConn(b.ep, 1800900206, "192.0.2.6:1")
	isole := newTestConn(b.ep, 1800900207, "192.0.2.7:1")
	demandeur := newTestConn(b.ep, 1800900210, "198.51.100.10:1")

	gid := b.creer(hote, 0x51, "").ID
	if r := b.rejoindre(invite, gid, ""); r == nil || r.IsError {
		t.Fatalf("jonction : %+v", r)
	}
	gidProtege := b.creer(protege, 0x51, mdpArene).ID

	// Partie rapide : salon cree par AutoMatchmake (0x28), jamais une arene.
	p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 2}, GameMode: 0x9001, OpenParticipation: true, Attribs: []uint32{0, 0, 0, 0, 0, 0}}}
	body := NewStreamOut(b.s)
	body.Add(p)
	if r := b.ext(rapide, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 4, body.Bytes())); r == nil || r.IsError {
		t.Fatalf("partie rapide : %+v", r)
	}

	gidFerme := b.creer(ferme, 0x51, "").ID
	gidTournoi := b.creer(tournoi, 0x51, "").ID
	gidIsole := b.creer(isole, 0x54000001, "").ID
	b.mm.mu.Lock()
	b.mm.gatherings[gidFerme].session.OpenParticipation = false
	b.mm.gatherings[gidTournoi].session.Codeword = "T-0000000007-00002"
	if len(b.mm.gatherings[gid].session.SessionKey) == 0 {
		b.mm.mu.Unlock()
		t.Fatal("l arene doit avoir une cle de session (sinon le test ne prouve rien)")
	}
	b.mm.mu.Unlock()
	_ = gidIsole

	res := b.arenesAmis(demandeur, 1800900201, 1800900202, 1800900203, 1800900204, 1800900205, 1800900206, 1800900207, 1800900299)
	trouve := map[uint64]uint32{}
	for _, r := range res {
		if r.Session.UserPassword != "" || len(r.Session.SessionKey) != 0 {
			t.Errorf("pid %d : mot de passe (%d o) ou cle (%d o) divulgue", r.PrincipalID, len(r.Session.UserPassword), len(r.Session.SessionKey))
		}
		trouve[r.PrincipalID] = r.Session.ID
	}
	if len(res) != 3 || trouve[1800900201] != gid || trouve[1800900202] != gid || trouve[1800900203] != gidProtege {
		t.Fatalf("attendu hote+invite dans gid %d et l arene protegee %d, recu %v", gid, gidProtege, trouve)
	}
	for _, r := range res {
		if r.Session.ID == gidProtege && !r.Session.UserPasswordEnabled {
			t.Error("le cadenas de l arene protegee doit rester")
		}
	}
	if res[0].Session.ID > res[len(res)-1].Session.ID {
		t.Error("arenes attendues dans l ordre des gid")
	}

	// La cle de l arene reste intacte dans le store.
	b.mm.mu.Lock()
	cle := len(b.mm.gatherings[gid].session.SessionKey)
	b.mm.mu.Unlock()
	if cle == 0 {
		t.Fatal("l expurgation doit porter sur une copie")
	}
}

// TestArenesAmisParNSA : une vraie Switch designe ses amis par leur NSA ; la traduction
// apprise a l authentification retrouve le PID, et l entree rendue porte l identifiant
// demande.
func TestArenesAmisParNSA(t *testing.T) {
	b := nouveauBancSmash(t)
	hote := newTestConn(b.ep, 1800900221, "192.0.2.21:1")
	gid := b.creer(hote, 0x51, "").ID
	const nsa uint64 = 1810090000000000021
	RememberLoginName("1810090000000000021", 1800900221)

	res := b.arenesAmis(newTestConn(b.ep, 1800900222, "198.51.100.22:1"), nsa, nsa, 0)
	if len(res) != 1 || res[0].PrincipalID != nsa || res[0].Session.ID != gid {
		t.Fatalf("attendu une entree {nsa, gid %d}, recu %+v", gid, res)
	}
}

// TestArenesAmisAutresJeux : sans la cle d acces de Smash, ou avec SansArenesAmis, la liste
// reste vide ; Animal Crossing (FindByParticipantEnabled) garde sa propre reponse.
func TestArenesAmisAutresJeux(t *testing.T) {
	b := nouveauBancArene(t) // cle de test, pas celle de Smash
	hote := newTestConn(b.ep, 1800900231, "192.0.2.31:1")
	b.creer(hote, 0x51, "")
	if res := b.arenesAmis(newTestConn(b.ep, 1800900232, "198.51.100.32:1"), 1800900231); len(res) != 0 {
		t.Fatalf("autre jeu : liste vide attendue, recu %d", len(res))
	}

	s := nouveauBancSmash(t)
	s.mm.SansArenesAmis = true
	s.creer(newTestConn(s.ep, 1800900233, "192.0.2.33:1"), 0x51, "")
	if res := s.arenesAmis(newTestConn(s.ep, 1800900234, "198.51.100.34:1"), 1800900233); len(res) != 0 {
		t.Fatalf("SansArenesAmis : liste vide attendue, recu %d", len(res))
	}
}

// TestArenesAmisRequeteIllisibleOuEnorme : un corps forge rend une liste vide sans planter ;
// plus de 300 identifiants sont tronques.
func TestArenesAmisRequeteIllisibleOuEnorme(t *testing.T) {
	b := nouveauBancSmash(t)
	c := newTestConn(b.ep, 1800900241, "198.51.100.41:1")
	for _, corps := range [][]byte{nil, {0}, {0, 0xff, 0xff, 0xff, 0xff}, {0, 8, 0, 0, 0, 0xff, 0xff, 0xff, 0x7f}} {
		resp := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodFindByParticipant, 1, corps))
		if resp == nil || resp.IsError || len(resp.Body) != 4 {
			t.Fatalf("corps %x : liste vide attendue, recu %+v", corps, resp)
		}
	}

	hote := newTestConn(b.ep, 1800900242, "192.0.2.42:1")
	b.creer(hote, 0x51, "")
	ids := make([]uint64, 400)
	for i := range ids {
		ids[i] = 1800900500 + uint64(i)
	}
	ids[350] = 1800900242 // au-dela de la borne : ignore
	if res := b.arenesAmis(c, ids...); len(res) != 0 {
		t.Fatalf("identifiant au-dela de 300 : ignore attendu, recu %d", len(res))
	}
	ids[10] = 1800900242
	if res := b.arenesAmis(c, ids...); len(res) != 1 {
		t.Fatalf("identifiant dans la borne : 1 arene attendue, recu %d", len(res))
	}
}

// TestArenesAmisOctetsPourEmulation ecrit une reponse reelle (deux arenes) dans le fichier
// nomme par NEXTENDO_EMU_51, pour la passer dans l extracteur du jeu 0x253ae0
// (un outil interne). Sans la variable, rien n est ecrit.
func TestArenesAmisOctetsPourEmulation(t *testing.T) {
	chemin := os.Getenv("NEXTENDO_EMU_51")
	if chemin == "" {
		t.Skip("NEXTENDO_EMU_51 absent")
	}
	b := nouveauBancSmash(t)
	h1 := newTestConn(b.ep, 1800900251, "192.0.2.51:1")
	h2 := newTestConn(b.ep, 1800900252, "192.0.2.52:1")
	b.creer(h1, 0x51, "")
	b.creer(h2, 0x51, mdpArene)
	resp := b.ext(newTestConn(b.ep, 1800900253, "198.51.100.53:1"),
		NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodFindByParticipant, 1, corpsParParticipant(b.s, 1800900251, 1800900252)))
	if err := os.WriteFile(chemin, resp.Body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d octets : %s", len(resp.Body), hex.EncodeToString(resp.Body))
}
