package nex

import "testing"

// TestFindByParticipantRendLIdentifiantDemande : avec FindByParticipantEchoRequestedID,
// une session trouvee via l'identifiant de la liste d'amis (NSA) revient sous CET
// identifiant — participant, proprietaire, hote — et non sous le PID interne, que la
// console du demandeur ne reconnait pas comme son ami (aucune JoinSession, 2026-10-05).
// La session STOCKEE, celle que voit l'hote, ne change pas.
func TestFindByParticipantRendLIdentifiantDemande(t *testing.T) {
	s := acnhSettings()
	m := NewMatchmaking()
	m.FindByParticipantEnabled = true
	m.FindByParticipantEchoRequestedID = true
	const nsa uint64 = 10000000000000001
	const hote uint64 = 1800009001
	m.FindByParticipantIDResolver = func(id uint64) uint64 {
		if id == nsa {
			return hote
		}
		return id
	}
	sess := &MatchmakeSession{GameMode: 1, OpenParticipation: true}
	sess.ID, sess.OwnerPID, sess.HostPID = 26, hote, hote
	m.gatherings[26] = &gathering{session: sess, participants: []uint64{hote}}

	cherche := func(ids ...uint64) []*FindMatchmakeSessionByParticipantResult {
		req := NewStreamOut(s)
		req.Add(&FindMatchmakeSessionByParticipantParam{PrincipalIDs: ids, Options: 3})
		rep := m.findByParticipant(&Connection{Settings: s, PID: 1800009002},
			NewRMCRequest(s, ProtocolMatchmakeExtension, MethodFindByParticipant, 1, req.Bytes()))
		in := NewStreamIn(rep.Body, s)
		return ReadList(in, func(i *StreamIn) *FindMatchmakeSessionByParticipantResult {
			var r FindMatchmakeSessionByParticipantResult
			i.Extract(&r)
			return &r
		})
	}

	r := cherche(nsa)
	if len(r) != 1 || r[0].PrincipalID != nsa || r[0].Session.OwnerPID != nsa || r[0].Session.HostPID != nsa {
		t.Fatalf("par NSA : %+v, attendu participant/proprietaire/hote = NSA", r)
	}
	if sess.OwnerPID != hote || sess.HostPID != hote {
		t.Fatal("la session stockee (celle de l'hote) a ete modifiee")
	}
	// Recherche par le PID interne lui-meme : rien ne change.
	if r := cherche(hote); len(r) != 1 || r[0].PrincipalID != hote || r[0].Session.OwnerPID != hote {
		t.Fatalf("par PID : %+v", r)
	}
}
