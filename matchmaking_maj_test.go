package nex

import (
	"encoding/binary"
	"testing"
)

// majDuJeu : vecteur 0x6D.0x2C du jeu (emu_maj.py) avec le gid de la session (u32 apres
// l'en-tete de Structure, 0x256e44).
func majDuJeu(t *testing.T, nom string, gid uint32) []byte {
	b := append([]byte(nil), vecteurAppariement(t, nom)...)
	binary.LittleEndian.PutUint32(b[5:], gid)
	return b
}

func sessionPourMaj(t *testing.T, mm *Matchmaking, conn *Connection, mode uint32) uint32 {
	s := testSettings()
	p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MinParticipants: 2, MaxParticipants: 2}, GameMode: mode,
		OpenParticipation: true, MatchmakeSystem: 1, Attribs: []uint32{2, 11, 0, 0, 0, 0}}}
	o := NewStreamOut(s)
	o.Add(p)
	var r MatchmakeSession
	NewStreamIn(mm.ExtensionHandler()(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
	return r.ID
}

// TestMajSessionSmash : la mise a jour du jeu (Smash a 4, tranche 12, Elite, rouverte) est
// appliquee par le proprietaire ; coupee, rien ne change ; un autre PID ou une session isolee
// ne change rien ; le GameMode (drapeau absent) ne bouge pas.
func TestMajSessionSmash(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		nom      string
		actif    bool
		autre    bool
		mode     uint32
		applique bool
	}{
		{"actif", true, false, 1, true},
		{"coupe", false, false, 1, false},
		{"autre PID", true, true, 1, false},
		{"session isolee", true, false, 0x54000001, false},
	} {
		ep := NewEndpoint(s)
		mm := NewMatchmaking()
		if cas.actif {
			mm.SessionPartSmash = func(*Connection) bool { return true }
		}
		hote := newTestConn(ep, 30, "198.51.100.30:1")
		gid := sessionPourMaj(t, mm, hote, cas.mode)
		mm.mu.Lock()
		mm.gatherings[gid].session.OpenParticipation = false // 0x6D.0x1 avant la mise a jour
		mm.mu.Unlock()
		appelant := hote
		if cas.autre {
			appelant = newTestConn(ep, 31, "198.51.100.31:1")
		}
		rep := mm.ExtensionHandler()(appelant, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 2, majDuJeu(t, "maj_smash4_ouverte", gid)))
		if rep.IsError || len(rep.Body) != 0 {
			t.Fatalf("%s : reponse erreur=%v corps=%x", cas.nom, rep.IsError, rep.Body)
		}
		mm.mu.Lock()
		ss := *mm.gatherings[gid].session
		mm.mu.Unlock()
		if cas.applique {
			if ss.MaxParticipants != 4 || !ss.OpenParticipation || ss.Attribs[1] != 12 || ss.Attribs[2] != 1 || ss.GameMode != cas.mode ||
				ss.Param.Params["_RU"].Uint != 0x10001 || !ss.Param.Params["@SR"].Bool || ss.ParticipationPolicy != 95 {
				t.Fatalf("%s : %+v", cas.nom, ss)
			}
			if _, vide := ss.Param.Params[""]; vide {
				t.Fatalf("%s : cle vide ecrite", cas.nom)
			}
		} else if ss.MaxParticipants != 2 || ss.OpenParticipation || ss.Attribs[1] != 11 {
			t.Fatalf("%s : la session a change : %+v", cas.nom, ss)
		}
	}
}

// TestMajSessionMaxSousPresents : un maximum sous le nombre de presents n'est pas ecrit.
func TestMajSessionMaxSousPresents(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.SessionPartSmash = func(*Connection) bool { return true }
	hote := newTestConn(ep, 30, "198.51.100.30:1")
	gid := sessionPourMaj(t, mm, hote, 1)
	mm.mu.Lock()
	g := mm.gatherings[gid]
	g.session.MaxParticipants = 4
	g.participants = append(g.participants, 31, 32)
	mm.mu.Unlock()
	mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 2, majDuJeu(t, "maj_1v1", gid)))
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if g.session.MaxParticipants != 4 {
		t.Fatalf("max ecrit sous 3 presents : %d", g.session.MaxParticipants)
	}
}
