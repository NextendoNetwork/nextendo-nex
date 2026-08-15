package nex

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Création d'un tournoi, relevée sur le serveur de Nintendo (Mario Kart 8
// Deluxe, capture du 2026-08-12). Le joueur crée un tournoi nommé « test » ; le
// client envoie la structure avec identifiant, propriétaire et code vides, et le
// serveur la rend complétée.
//
// Ces octets sont la seule référence dont on dispose : si notre transformation
// dérive, ce test tombe.

// requête réelle du client (sans l'en-tête version+longueur)
const captureCreateReq = "" +
	"000000000000000000000000140000000200000003000000010000000100000001000000020000000100000001000000" +
	"010000000100000001000000ffffffffffffffff00000000000000000000000000000000000000000000000000000000" +
	"4f005a5a0001006a020a0074006500730074000000040200000005040003000000060400040000000702000000080200" +
	"000003010000090400040000000a020000000b04008ead8b0f01040000000000ff000000000100000020000000000000" +
	"006009060000000000c0120000000019aa1f00000000101baa1f000000000000000000000000000000"

func capturedTournamentPayload(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(captureCreateReq)
	if err != nil {
		t.Fatalf("hex invalide : %v", err)
	}
	return b
}

// TestStampTournamentReproducesCapture : poser identifiant, propriétaire et code
// doit produire exactement ce que Nintendo a renvoyé — hors les trois octets de
// queue, un compteur que le client envoie à zéro et dont il ne dépend pas.
func TestStampTournamentReproducesCapture(t *testing.T) {
	req := capturedTournamentPayload(t)

	const (
		id   = uint32(3854367)
		code = "096942834219"
	)
	// Les 8 octets en +0x04 sont une marque propre au tournoi, PAS le PID du
	// créateur : celle rendue par Nintendo ne figure dans aucun PID connu du
	// joueur. On rejoue donc la sienne telle quelle.
	mark := []byte{0x4e, 0xc9, 0xeb, 0x25, 0xf0, 0xfa, 0x43, 0xef}
	got := stampTournament(req, id, mark, code)

	// La réponse fait 12 octets de plus que la requête : la chaîne de code vide
	// (3 octets) devient 12 chiffres plus terminateur (15 octets).
	if len(got) != len(req)+12 {
		t.Fatalf("longueur %d, attendu %d", len(got), len(req)+12)
	}
	// Identifiant et propriétaire posés en tête.
	if u := uint32(got[0]) | uint32(got[1])<<8 | uint32(got[2])<<16 | uint32(got[3])<<24; u != id {
		t.Errorf("identifiant = %d, attendu %d", u, id)
	}
	if !bytes.Equal(got[4:12], mark) {
		t.Errorf("marque = %x, attendu %x", got[4:12], mark)
	}
	// Le code doit être présent, précédé de sa longueur (12 + terminateur).
	if !bytes.Contains(got, append([]byte{13, 0}, append([]byte(code), 0)...)) {
		t.Error("le code n'a pas été posé au bon format")
	}
	// Tout ce qui précède l'identifiant de code doit être intact.
	if !bytes.Equal(got[12:0xb5], req[12:0xb5]) {
		t.Error("les règles, le nom et les équipes ont été altérés")
	}
}

// A later u32 value of 1 is also encoded as 01 00 00 00. The old "last empty
// string" heuristic inserted the code there and produced a shifted MK8D blob.
func TestStampTournamentIgnoresLaterEmptyPattern(t *testing.T) {
	req := capturedTournamentPayload(t)
	codeOffset := tournamentCodeOffset(req)
	if codeOffset != 0xb5 {
		t.Fatalf("code offset = %#x, want 0xb5", codeOffset)
	}
	req[0xd0], req[0xd1], req[0xd2], req[0xd3] = 1, 0, 0, 0

	const code = "123456789012"
	got := stampTournament(req, 4242, make([]byte, 8), code)
	if !tournamentCodeMatches(got, code) {
		t.Fatal("code was not written to its structural field")
	}
	// The later binary pattern must remain u32(1), not become a string. Writing
	// the code at the correct field shifts this later value by 12 bytes.
	if !bytes.Equal(got[0xd0+12:0xd4+12], []byte{1, 0, 0, 0}) {
		t.Fatalf("later field was modified: %x", got[0xd0+12:0xd4+12])
	}
}

// TestNewTournamentCode : douze chiffres, comme celui de Nintendo.
func TestNewTournamentCode(t *testing.T) {
	for i := 0; i < 50; i++ {
		c := newTournamentCode()
		if len(c) != tournamentCodeLen {
			t.Fatalf("code de %d caractères, attendu %d", len(c), tournamentCodeLen)
		}
		for _, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("caractère non numérique dans %q", c)
			}
		}
	}
}

// TestTournamentLifecycle : créer, retrouver, inscrire.
func TestTournamentLifecycle(t *testing.T) {
	m := NewMatchmaking()
	payload := capturedTournamentPayload(t)

	tr := m.CreateTournament(1800000006, payload)
	if tr == nil {
		t.Fatal("création refusée")
	}
	if tournamentByID(tr.ID) == nil {
		t.Error("tournoi introuvable après création")
	}
	if len(tr.Participants) != 1 || tr.Participants[0] != 1800000006 {
		t.Errorf("le créateur devrait être inscrit d'office : %v", tr.Participants)
	}
	if !joinTournament(tr.ID, 1800000119) {
		t.Error("inscription refusée")
	}
	if joinTournament(tr.ID, 1800000119); len(tournamentByID(tr.ID).Participants) != 2 {
		t.Error("une double inscription ne doit pas dupliquer le joueur")
	}
	if joinTournament(999999, 1800000119) {
		t.Error("inscription à un tournoi inexistant acceptée")
	}
}

// TestDeleteTournamentOwnerOnly : un tournoi ne peut être supprimé que par son
// propriétaire. Les identifiants sont des entiers croissants, donc devinables :
// sans ce contrôle, n'importe qui effacerait le tournoi d'un autre.
func TestDeleteTournamentOwnerOnly(t *testing.T) {
	m := NewMatchmaking()
	payload := capturedTournamentPayload(t)

	tr := m.CreateTournament(1800000006, payload)
	if tr == nil {
		t.Fatal("création refusée")
	}
	if deleteTournament(tr.ID, 1800000119) {
		t.Error("un tiers a pu supprimer le tournoi")
	}
	if tournamentByID(tr.ID) == nil {
		t.Fatal("le tournoi a disparu alors que la suppression était refusée")
	}
	if !deleteTournament(tr.ID, 1800000006) {
		t.Error("le propriétaire n'a pas pu supprimer son tournoi")
	}
	if tournamentByID(tr.ID) != nil {
		t.Error("le tournoi est toujours là après suppression")
	}
	if deleteTournament(tr.ID, 1800000006) {
		t.Error("une seconde suppression devrait échouer")
	}
}

func TestCreateTournamentRejectsInvalidPayloads(t *testing.T) {
	m := NewMatchmaking()
	badCode := capturedTournamentPayload(t)
	badCode[tournamentCodeOffset(badCode)] = 2 // The creator must send an empty code.
	oddName := capturedTournamentPayload(t)
	oddName[tournamentNameLengthOffset] = 3
	valid := capturedTournamentPayload(t)
	oversized := append(valid, make([]byte, tournamentMaxBlob+1-len(valid))...)

	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"truncated", make([]byte, tournamentNameLengthOffset+1)},
		{"odd name length", oddName},
		{"non-empty or malformed code field", badCode},
		{"oversized", oversized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tr := m.CreateTournament(1800000006, tc.payload); tr != nil {
				t.Fatal("invalid tournament payload was stored")
			}
		})
	}
}

func TestWriteTournamentListIsolatesInvalidEntry(t *testing.T) {
	s := testSettings()
	payload := capturedTournamentPayload(t)
	valid := (&Matchmaking{}).CreateTournament(1800000006, payload)
	if valid == nil {
		t.Fatal("valid tournament was rejected")
	}
	bad := *valid
	bad.Blob = append([]byte(nil), valid.Blob...)
	bad.Blob[tournamentCodeOffset(bad.Blob)+2] = 'X'

	body := writeTournamentList(s, []*Tournament{&bad, valid})
	in := NewStreamIn(body, s)
	if n := in.U32(); n != 1 {
		t.Fatalf("list declares %d entries, want 1", n)
	}
}
