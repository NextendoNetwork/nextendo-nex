package nex

import "testing"

// corpsUpdateSessionHost : la requete 0x15.0x2A telle que l'ecrit le jeu (0x24bcb0 : u32 gid en
// 0x24bdb0, puis le booleen isMigrateOwner, 1 en 0x1100dc).
func corpsUpdateSessionHost(gid uint32, proprietaire bool) []byte {
	b := []byte{byte(gid), byte(gid >> 8), byte(gid >> 16), byte(gid >> 24), 0}
	if proprietaire {
		b[4] = 1
	}
	return b
}

// TestMigrationHoteApresDepart : sequence de prod (salon 201 du 16/10) : le proprietaire sort
// (0x32.0x1), un participant appelle UpdateSessionHost ; il devient hote et proprietaire, recoit
// la reponse vide puis le 4000 a son nom (Param1 = gid, Param2 = lui), ce qu'attend 0x1101a0,
// et pas de 110000 (Nintendo ne l'envoie pas a l'appelant). Coupe : rien ne change, la reponse
// est rendue par le gestionnaire comme avant.
func TestMigrationHoteApresDepart(t *testing.T) {
	s := testSettings()
	for _, actif := range []bool{true, false} {
		ep := NewEndpoint(s)
		mm := NewMatchmaking()
		mm.Endpoint = ep
		if actif {
			mm.MigrationHote = func(uint64) bool { return true }
		}
		ext := mm.ExtensionHandler()
		auto := func(conn *Connection) uint32 {
			p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 2}, GameMode: 1, OpenParticipation: true}}
			o := NewStreamOut(s)
			o.Add(p)
			var r MatchmakeSession
			NewStreamIn(ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
			return r.ID
		}
		hote := newTestConn(ep, 10, "198.51.100.10:1")
		suiv, capSuiv := connCapte(ep, 11, "198.51.100.11:1")
		gid := auto(hote)
		if auto(suiv) != gid {
			t.Fatal("le second joueur doit rejoindre le salon")
		}
		o := NewStreamOut(s)
		o.U32(gid)
		mm.MatchMakingExtHandler()(hote, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 2, o.Bytes()))
		avant := len(rmcRecus(t, s, capSuiv))
		rep := mm.MatchMakingHandler()(suiv, NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 3, corpsUpdateSessionHost(gid, true)))
		mm.mu.Lock()
		g := mm.gatherings[gid]
		hoteSess, proprio, connHote := g.session.HostPID, g.session.OwnerPID, g.hostConnID
		mm.mu.Unlock()
		recus := recusDepuis(t, s, capSuiv, avant)
		if actif {
			if rep != nil {
				t.Fatalf("reponse rendue au lieu d'etre envoyee avant le 4000 : %+v", rep)
			}
			if hoteSess != 11 || proprio != 11 || connHote != suiv.ID {
				t.Fatalf("migration : hote=%d proprietaire=%d conn=%d", hoteSess, proprio, connHote)
			}
			if len(recus) != 2 || !recus[0].reponse || recus[1].ev.Type != NotificationOwnershipChanged ||
				recus[1].ev.Param1 != uint64(gid) || recus[1].ev.Param2 != 11 {
				t.Fatalf("migration : recu %+v", recus)
			}
		} else {
			if rep == nil || rep.IsError || len(rep.Body) != 0 {
				t.Fatalf("coupe : reponse %+v", rep)
			}
			if hoteSess != 10 || proprio != 10 || len(recus) != 0 {
				t.Fatalf("coupe : hote=%d proprietaire=%d recu %+v", hoteSess, proprio, recus)
			}
		}
	}
}

// TestMigrationHoteRefuseeHorsSalon : un PID hors du salon ne prend pas l'hote.
func TestMigrationHoteRefuseeHorsSalon(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.MigrationHote = func(uint64) bool { return true }
	ext := mm.ExtensionHandler()
	hote := newTestConn(ep, 10, "198.51.100.10:1")
	p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 2}, GameMode: 1, OpenParticipation: true}}
	o := NewStreamOut(s)
	o.Add(p)
	var r MatchmakeSession
	NewStreamIn(ext(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
	intrus := newTestConn(ep, 99, "198.51.100.99:1")
	mm.MatchMakingHandler()(intrus, NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 2, corpsUpdateSessionHost(r.ID, true)))
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if g := mm.gatherings[r.ID]; g.session.HostPID != 10 || g.session.OwnerPID != 10 {
		t.Fatalf("hote=%d proprietaire=%d", g.session.HostPID, g.session.OwnerPID)
	}
}

// TestSalonGardeQuandProprietaireDeconnecte : migration active, le proprietaire se deconnecte,
// le salon reste pour le successeur ; coupee, il est supprime comme avant ; vide, il disparait.
func TestSalonGardeQuandProprietaireDeconnecte(t *testing.T) {
	s := testSettings()
	for _, actif := range []bool{true, false} {
		ep := NewEndpoint(s)
		mm := NewMatchmaking()
		if actif {
			mm.MigrationHote = func(uint64) bool { return true }
		}
		ext := mm.ExtensionHandler()
		auto := func(conn *Connection) uint32 {
			p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, GameMode: 1, OpenParticipation: true}}
			o := NewStreamOut(s)
			o.Add(p)
			var r MatchmakeSession
			NewStreamIn(ext(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
			return r.ID
		}
		a := newTestConn(ep, 20, "198.51.100.20:1")
		b := newTestConn(ep, 21, "198.51.100.21:1")
		gid := auto(a)
		auto(b)
		mm.RemovePlayer(20)
		mm.mu.Lock()
		_, reste := mm.gatherings[gid]
		mm.mu.Unlock()
		if reste != actif {
			t.Fatalf("actif=%v : salon present=%v", actif, reste)
		}
		mm.RemovePlayer(21)
		mm.mu.Lock()
		_, reste = mm.gatherings[gid]
		mm.mu.Unlock()
		if reste {
			t.Fatal("salon vide garde")
		}
	}
}

// TestSalonOrphelinHorsAppariement : un salon garde pour la migration n'est propose a personne
// tant que le successeur n'a pas appele UpdateSessionHost ; ensuite il redevient joignable.
func TestSalonOrphelinHorsAppariement(t *testing.T) {
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
	a := newTestConn(ep, 40, "198.51.100.40:1")
	b := newTestConn(ep, 41, "198.51.100.41:1")
	gid := auto(a)
	auto(b)
	ep.unregisterConnection(a)
	mm.RemovePlayer(40)
	if g := auto(newTestConn(ep, 42, "198.51.100.42:1")); g == gid {
		t.Fatal("salon orphelin propose a un arrivant")
	}
	mm.RemovePlayer(42)
	// Jonction par le gid (0x6D.0x27) : refusee comme pour un salon supprime (URL vides sinon) ;
	// un participant deja present garde sa place.
	if r := ext(newTestConn(ep, 44, "198.51.100.44:1"), NewRMCRequest(s, ProtocolMatchmakeExtension, MethodJoinMatchmakeSessionWithParam, 3, corpsRejoindre(s, gid, ""))); r == nil || !r.IsError {
		t.Fatalf("jonction d'un salon orphelin par son gid : %+v", r)
	}
	if r := ext(b, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodJoinMatchmakeSessionWithParam, 3, corpsRejoindre(s, gid, ""))); r == nil || r.IsError {
		t.Fatalf("participant du salon orphelin refuse : %+v", r)
	}
	mm.MatchMakingHandler()(b, NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 2, corpsUpdateSessionHost(gid, true)))
	if g := auto(newTestConn(ep, 43, "198.51.100.43:1")); g != gid {
		t.Fatalf("apres la migration, le salon %d devait etre rejoint, obtenu %d", gid, g)
	}
}
