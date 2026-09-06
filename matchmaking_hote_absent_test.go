package nex

import "testing"

// TestPrefereSalonAvecHoteEnLigne fixe la regle de choix quand deux salons conviennent :
// celui dont l'hote est joignable passe devant.
//
// Le defaut qu'elle attrape : un joueur deconnecte garde sa place quelques secondes, le
// temps que sa console rouvre sa connexion. Son salon reste donc joignable, et un arrivant
// pouvait s'y installer pour attendre quelqu'un qui ne revenait pas. Mesure du 2026-09-02 :
// le versus de SMM2 ne demarrait qu'au DEUXIEME essai, le premier etant passe a attendre
// dans le salon d'un absent.
func TestPrefereSalonAvecHoteEnLigne(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext := mm.ExtensionHandler()

	auto := func(conn *Connection, mode uint32) *MatchmakeSession {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering:         Gathering{MaxParticipants: 4},
			GameMode:          mode,
			OpenParticipation: true,
			Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		}}
		body := NewStreamOut(s)
		body.Add(p)
		resp := ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
		var out MatchmakeSession
		NewStreamIn(resp.Body, s).Extract(&out)

		return &out
	}

	// Deux hotes ouvrent chacun leur salon. Ils passent par des modes differents, sinon le
	// second rejoindrait simplement le premier ; on ramene ensuite le second au meme mode
	// pour que l'arrivant ait un VRAI choix entre les deux.
	absent := newTestConn(ep, 1000, "88.0.0.1:1")
	present := newTestConn(ep, 2000, "88.0.0.2:1")
	_ = auto(absent, 11)
	salonPresent := auto(present, 12)
	mm.mu.Lock()
	mm.gatherings[salonPresent.ID].session.GameMode = 11
	mm.mu.Unlock()

	// Le premier hote disparait, mais son salon survit encore (delai de grace).
	ep.unregisterConnection(absent)

	arrivant := newTestConn(ep, 3000, "88.0.0.3:1")
	choisi := auto(arrivant, 11)
	if choisi.ID != salonPresent.ID {
		t.Errorf("l'arrivant devait rejoindre le salon de l'hote EN LIGNE (%d), il a pris %d",
			salonPresent.ID, choisi.ID)
	}
}

// TestSalonHoteAbsentSertDeRepli est l'autre moitie de la regle, et elle compte autant :
// la preference ne doit PAS devenir un refus.
//
// Exiger un hote connecte renverrait chacun dans son propre salon des que l'hote cligne des
// yeux — la fragmentation exacte qu'on venait de corriger, ou tout le monde cherche en meme
// temps et personne ne se rencontre. Sans cette epreuve, durcir la regle passerait pour un
// resserrement prudent.
func TestSalonHoteAbsentSertDeRepli(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext := mm.ExtensionHandler()

	auto := func(conn *Connection, mode uint32) *MatchmakeSession {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering:         Gathering{MaxParticipants: 4},
			GameMode:          mode,
			OpenParticipation: true,
			Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		}}
		body := NewStreamOut(s)
		body.Add(p)
		resp := ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
		var out MatchmakeSession
		NewStreamIn(resp.Body, s).Extract(&out)

		return &out
	}

	hote := newTestConn(ep, 1000, "88.0.0.1:1")
	salon := auto(hote, 11)
	ep.unregisterConnection(hote) // seul salon disponible, et son hote est absent

	arrivant := newTestConn(ep, 2000, "88.0.0.2:1")
	choisi := auto(arrivant, 11)
	if choisi.ID != salon.ID {
		t.Errorf("faute de mieux l'arrivant devait rejoindre le salon %d, il a ouvert %d",
			salon.ID, choisi.ID)
	}
}

// TestFermetureRetireLesInjoignables : fermer la participation fige la composition du match,
// donc un participant devenu injoignable doit en sortir.
//
// Le defaut attrape : le delai de grace garde la place d'un joueur qui reconnecte, ce qui est
// bon pendant la recherche. Mais si ce joueur est encore absent quand le salon se ferme, les
// autres demarrent une partie en l'attendant, et elle ne se termine jamais. Mesure du
// 2026-09-02 : salon annonce a quatre, trois consoles seulement appelant StartBattleMode.
func TestFermetureRetireLesInjoignables(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext := mm.ExtensionHandler()

	auto := func(conn *Connection) uint32 {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering:         Gathering{MaxParticipants: 4},
			GameMode:          11,
			OpenParticipation: true,
			Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		}}
		body := NewStreamOut(s)
		body.Add(p)
		resp := ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
		var out MatchmakeSession
		NewStreamIn(resp.Body, s).Extract(&out)

		return out.ID
	}

	hote := newTestConn(ep, 1000, "88.0.0.1:1")
	parti := newTestConn(ep, 2000, "88.0.0.2:1")
	reste := newTestConn(ep, 3000, "88.0.0.3:1")
	gid := auto(hote)
	auto(parti)
	auto(reste)

	mm.mu.Lock()
	avant := len(mm.gatherings[gid].participants)
	mm.mu.Unlock()
	if avant != 3 {
		t.Fatalf("montage : trois participants attendus, %d obtenus", avant)
	}

	// L'un d'eux disparait, mais garde sa place (delai de grace).
	ep.unregisterConnection(parti)

	body := NewStreamOut(s)
	body.U32(gid)
	ext(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCloseParticipation, 2, body.Bytes()))

	mm.mu.Lock()
	apres := append([]uint64(nil), mm.gatherings[gid].participants...)
	mm.mu.Unlock()

	if len(apres) != 2 {
		t.Errorf("la fermeture devait laisser 2 joueurs joignables, elle en laisse %d (%v)", len(apres), apres)
	}
	for _, pid := range apres {
		if pid == 2000 {
			t.Error("le participant injoignable est reste dans la composition du match")
		}
	}
	// L'hote qui ferme ne doit jamais s'auto-exclure : c'est lui qui parle.
	if len(apres) > 0 && apres[0] != 1000 {
		t.Errorf("l'hote devait rester en tete, on a %v", apres)
	}
}

// TestHebergementMigreImmediatement : un salon ne doit jamais rester heberge par quelqu'un
// d'injoignable, meme pendant le delai de grace.
//
// Sans ca, GetSessionURLs renvoie une liste de stations vide, la console lance sa migration
// d'hote et plante dedans (rapport Atmosphere du 2026-09-02, Data Abort sur une racine de
// collection a 0x10). Le delai de grace couvre l'APPARTENANCE, jamais l'HEBERGEMENT.
func TestHebergementMigreImmediatement(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext := mm.ExtensionHandler()

	auto := func(conn *Connection) uint32 {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering:         Gathering{MaxParticipants: 4},
			GameMode:          11,
			OpenParticipation: true,
			Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		}}
		body := NewStreamOut(s)
		body.Add(p)
		resp := ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes()))
		var out MatchmakeSession
		NewStreamIn(resp.Body, s).Extract(&out)

		return out.ID
	}

	hote := newTestConn(ep, 1000, "88.0.0.1:1")
	autre := newTestConn(ep, 2000, "88.0.0.2:1")
	gid := auto(hote)
	auto(autre)

	ep.unregisterConnection(hote)
	if n := mm.MigrerHebergementDepuis(1000, ep); n != 1 {
		t.Fatalf("un salon devait changer d'hote, %d migre(s)", n)
	}

	mm.mu.Lock()
	g := mm.gatherings[gid]
	hostPID, ownerPID, connID, tete := g.session.HostPID, g.session.OwnerPID, g.hostConnID, g.participants[0]
	mm.mu.Unlock()

	if hostPID != 2000 || ownerPID != 2000 {
		t.Errorf("hote/proprietaire devaient passer a 2000, on a %d/%d", hostPID, ownerPID)
	}
	if connID != autre.ID {
		t.Errorf("hostConnID devait suivre la connexion joignable (%d), il vaut %d", autre.ID, connID)
	}
	if tete != 2000 {
		t.Errorf("le repreneur doit etre en tete des participants, on a %d", tete)
	}

	// L'ancien hote garde sa PLACE : c'est tout l'interet du delai de grace.
	mm.mu.Lock()
	present := containsPID(mm.gatherings[gid].participants, 1000)
	mm.mu.Unlock()
	if !present {
		t.Error("migrer l'hebergement ne doit pas retirer l'ancien hote du salon")
	}
}
