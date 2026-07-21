package nex

import (
	"testing"
)

// Le risque d'un portage de protocole n'est pas qu'il compile, c'est que les octets soient
// faux : une structure mal ordonnée décale tout ce qui suit et le jeu affiche un code
// d'erreur incompréhensible. Ces tests figent donc l'ordre des champs tel que documenté par
// NintendoClients (MIT), et vérifient que chaque structure se relit elle-même à l'identique.

func acnhSettings() *Settings {
	s := NewSwitchSettings("v43a10em", 40000)
	return s
}

func TestSearchCriteriaRoundTrip(t *testing.T) {
	s := acnhSettings()
	want := &MatchmakeSessionSearchCriteria{
		Attribs:             []string{"1", "", "3"},
		GameMode:            "2",
		MinParticipants:     "1",
		MaxParticipants:     "8",
		MatchmakeSystem:     "1",
		VacantOnly:          true,
		ExcludeLocked:       true,
		ExcludeNonHostPID:   false,
		SelectionMethod:     3,
		VacantParticipants:  2,
		ExcludeUserPassword: true,
		ExcludeSysPassword:  false,
		ReferGID:            42,
		Codeword:            "ABC12", // un Dodo Code fait 5 caractères
	}

	out := NewStreamOut(s)
	out.Add(want)

	var got MatchmakeSessionSearchCriteria
	NewStreamIn(out.Bytes(), s).Extract(&got)

	if got.Codeword != want.Codeword {
		t.Fatalf("Codeword : got %q, want %q — le Dodo Code est le champ qui porte tout ACNH", got.Codeword, want.Codeword)
	}
	if got.GameMode != want.GameMode || got.MaxParticipants != want.MaxParticipants {
		t.Fatalf("champs texte décalés : got gameMode=%q max=%q", got.GameMode, got.MaxParticipants)
	}
	if got.SelectionMethod != want.SelectionMethod || got.ReferGID != want.ReferGID {
		t.Fatalf("champs numériques décalés : got selection=%d referGID=%d", got.SelectionMethod, got.ReferGID)
	}
	if got.VacantParticipants != want.VacantParticipants {
		t.Fatalf("VacantParticipants (bloc 3.5.0) : got %d, want %d", got.VacantParticipants, want.VacantParticipants)
	}
	if len(got.Attribs) != len(want.Attribs) {
		t.Fatalf("Attribs : got %d entrées, want %d", len(got.Attribs), len(want.Attribs))
	}
}

func TestFindByParticipantParamRoundTrip(t *testing.T) {
	s := acnhSettings()
	want := &FindMatchmakeSessionByParticipantParam{
		PrincipalIDs: []uint64{1800001659, 1800001660},
		Options:      7,
		BlockList:    MatchmakeBlockListParam{Options: 3},
	}

	out := NewStreamOut(s)
	out.Add(want)

	var got FindMatchmakeSessionByParticipantParam
	NewStreamIn(out.Bytes(), s).Extract(&got)

	if len(got.PrincipalIDs) != 2 || got.PrincipalIDs[0] != 1800001659 || got.PrincipalIDs[1] != 1800001660 {
		t.Fatalf("PrincipalIDs : got %v", got.PrincipalIDs)
	}
	if got.Options != want.Options {
		t.Fatalf("Options : got %d, want %d", got.Options, want.Options)
	}
	if got.BlockList.Options != want.BlockList.Options {
		t.Fatalf("BlockList : got %d, want %d", got.BlockList.Options, want.BlockList.Options)
	}
}

// La réponse est une liste de paires {PID, session}. Renvoyer des sessions NUES décale la
// lecture côté client — il lit la session là où il attend le PID — et ACNH tombe en
// 2306-0116. Ce test verrouille la présence du PID en tête de chaque entrée.
func TestFindByParticipantResultCarriesPIDFirst(t *testing.T) {
	s := acnhSettings()
	sess := MatchmakeSession{GameMode: 4, OpenParticipation: true, Codeword: "ZZ999"}
	sess.ID = 77
	sess.OwnerPID = 1800001659

	want := &FindMatchmakeSessionByParticipantResult{PrincipalID: 1800001659, Session: sess}
	out := NewStreamOut(s)
	out.Add(want)

	var got FindMatchmakeSessionByParticipantResult
	NewStreamIn(out.Bytes(), s).Extract(&got)

	if got.PrincipalID != want.PrincipalID {
		t.Fatalf("PrincipalID : got %d, want %d", got.PrincipalID, want.PrincipalID)
	}
	if got.Session.ID != 77 || got.Session.OwnerPID != 1800001659 {
		t.Fatalf("session décalée : gid=%d owner=%d", got.Session.ID, got.Session.OwnerPID)
	}
	if got.Session.Codeword != "ZZ999" {
		t.Fatalf("Codeword de session perdu : %q", got.Session.Codeword)
	}
}

// Une visite d'ami doit trouver la session de l'hôte ; un ami sans île ouverte ne doit
// produire AUCUNE entrée (et surtout pas une erreur, qui afficherait un code au joueur).
func TestFindByParticipantFindsHostSession(t *testing.T) {
	m := NewMatchmaking()
	m.FindByParticipantEnabled = true

	host := uint64(1800001659)
	sess := &MatchmakeSession{GameMode: 1, OpenParticipation: true}
	sess.OwnerPID = host
	m.gatherings[1] = &gathering{session: sess, participants: []uint64{host}}
	sess.ID = 1

	if g := m.sessionOfParticipant(host); g == nil {
		t.Fatal("la session de l'hôte est introuvable : la visite d'ami renverrait « aucune île »")
	}
	if g := m.sessionOfParticipant(1800009999); g != nil {
		t.Fatal("un joueur sans session ne doit correspondre à aucun rassemblement")
	}
}

// Le défaut doit rester la liste vide : Smash appelle 0x33 au démarrage et s'appuie dessus.
func TestFindByParticipantDisabledByDefault(t *testing.T) {
	if NewMatchmaking().FindByParticipantEnabled {
		t.Fatal("FindByParticipantEnabled doit être false par défaut (Smash/MK8/S2 inchangés)")
	}
}

// Le Dodo Code n'arrive QUE par la mise à jour partielle de session. S'il n'est pas écrit,
// la recherche par code ne peut jamais correspondre et le visiteur voit « aucune île ».
func TestSessionPartPersistsCodeword(t *testing.T) {
	m := NewMatchmaking()
	m.SessionPartPersists = true

	sess := &MatchmakeSession{OpenParticipation: true}
	sess.ID = 5
	sess.OwnerPID = 1800001659
	m.gatherings[5] = &gathering{session: sess, participants: []uint64{1800001659}}

	// Un vrai Dodo Code arrive avec flags 0x4002 (OpenParticipation | Codeword).
	if !m.applySessionPart(&UpdateMatchmakeSessionParam{GID: 5, ModificationFlags: 0x4002, Codeword: "DODO1", OpenParticipation: true}) {
		t.Fatal("la mise à jour partielle n'a pas trouvé la session")
	}
	if m.gatherings[5].session.Codeword != "DODO1" || m.byCode["DODO1"] != 5 {
		t.Fatalf("Dodo Code non enregistré : code=%q byCode=%d", m.gatherings[5].session.Codeword, m.byCode["DODO1"])
	}
	if !m.gatherings[5].session.OpenParticipation {
		t.Fatal("OpenParticipation aurait dû être appliqué (bit 0x2 posé)")
	}

	// Un 0x2C SANS le bit OpenParticipation ne doit PAS refermer la porte.
	m.applySessionPart(&UpdateMatchmakeSessionParam{GID: 5, ModificationFlags: 0, OpenParticipation: false})
	if !m.gatherings[5].session.OpenParticipation {
		t.Fatal("la porte a été refermée par un 0x2C sans le drapeau OpenParticipation")
	}
}

// Le défaut doit rester le simple acquittement, pour ne rien changer à la fin de coop S2.
func TestSessionPartDoesNotPersistByDefault(t *testing.T) {
	if NewMatchmaking().SessionPartPersists {
		t.Fatal("SessionPartPersists doit être false par défaut (Splatoon 2 inchangé)")
	}
}

// Le menu de visite se remplit des amis AYANT publié une donnée ; les autres n'y sont pas,
// et un joueur déconnecté doit en disparaître.
func TestFriendNotificationData(t *testing.T) {
	m := NewMatchmaking()
	const me, open, closed = uint64(1800000001), uint64(1800000002), uint64(1800000003)
	m.FriendPIDs = func(pid uint64) []uint64 { return []uint64{open, closed} }

	m.notif.put(open, 101, &NotificationEvent{PIDSource: open, Type: 101, Param1: 42, Param2: 1})    // porte OUVERTE
	m.notif.put(closed, 101, &NotificationEvent{PIDSource: closed, Type: 101, Param1: 7, Param2: 0}) // porte FERMÉE

	// Seul l'ami à la porte ouverte doit être proposé ; le fermé (Param2=0) est filtré.
	if got := m.friendNotificationsFor(me, []uint32{101}); len(got) != 1 || got[0].PIDSource != open {
		t.Fatalf("seul l'ami à la porte ouverte doit être proposé (fermé filtré) : %+v", got)
	}
	if got := m.friendNotificationsFor(me, []uint32{999}); len(got) != 0 {
		t.Fatalf("un autre type ne doit rien renvoyer : %+v", got)
	}

	m.RemovePlayer(open)
	if got := m.friendNotificationsFor(me, []uint32{101}); len(got) != 0 {
		t.Fatalf("un ami déconnecté ne doit plus être proposé : %+v", got)
	}
}

// Sans source d'amis configurée (MK8, S2, Smash), la réponse reste vide : rien ne change.
func TestFriendNotificationsEmptyWithoutSource(t *testing.T) {
	m := NewMatchmaking()
	m.notif.put(1800000002, 101, &NotificationEvent{Type: 101})
	if got := m.friendNotificationsFor(1800000001, []uint32{101}); len(got) != 0 {
		t.Fatalf("sans FriendPIDs la liste doit rester vide : %+v", got)
	}
}
