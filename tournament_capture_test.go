package nex

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// Création d'un tournoi, relevée sur le serveur de Nintendo (Mario Kart 8
// Deluxe, capture du 2026-08-12). Le joueur crée un tournoi nommé « test » ; le
// client envoie la structure avec identifiant, propriétaire et code vides, et le
// serveur la rend complétée.
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

// TestStampTournamentReproducesCapture : poser identifiant, marque et code
// doit reproduire exactement la capture de Nintendo.
func TestStampTournamentReproducesCapture(t *testing.T) {
	req := capturedTournamentPayload(t)

	const (
		id   = uint32(3854367)
		code = "096942834219"
	)
	mark := []byte{0x4e, 0xc9, 0xeb, 0x25, 0xf0, 0xfa, 0x43, 0xef}
	got := stampTournament(req, id, mark, code)

	if len(got) != len(req)+12 {
		t.Fatalf("longueur %d, attendu %d", len(got), len(req)+12)
	}
	if u := binary.LittleEndian.Uint32(got[:4]); u != id {
		t.Errorf("identifiant = %d, attendu %d", u, id)
	}
	if !bytes.Equal(got[4:12], mark) {
		t.Errorf("marque = %x, attendu %x", got[4:12], mark)
	}
	if !bytes.Contains(got, append([]byte{13, 0}, append([]byte(code), 0)...)) {
		t.Error("le code n'a pas été posé au bon format")
	}
	if !bytes.Equal(got[12:0xb5], req[12:0xb5]) {
		t.Error("les règles, le nom et les équipes ont été altérés")
	}
}

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
	if !bytes.Equal(got[0xd0+12:0xd4+12], []byte{1, 0, 0, 0}) {
		t.Fatalf("later field was modified: %x", got[0xd0+12:0xd4+12])
	}
}

// TestTournamentVariableLengthName : teste un nom de longueur variable (ex: 20 caractères au lieu de 4)
func TestTournamentVariableLengthName(t *testing.T) {
	req := capturedTournamentPayload(t)
	// Remplacer le nom "test" (10 octets utf16) par "Mario Grand Prix 2026" (44 octets utf16)
	longName := "Mario Grand Prix 2026\x00"
	var longNameUTF16 []byte
	for _, r := range longName {
		longNameUTF16 = append(longNameUTF16, byte(r), 0)
	}
	newLen := len(longNameUTF16)

	// Reconstruire le payload avec le nouveau nom
	var custom []byte
	custom = append(custom, req[:0x69]...)
	lenBytes := make([]byte, 2)
	binary.LittleEndian.PutUint16(lenBytes, uint16(newLen))
	custom = append(custom, lenBytes...)
	custom = append(custom, longNameUTF16...)
	custom = append(custom, req[0x75:]...) // Règles et reste

	expectedOffset := 0x69 + 2 + newLen + (0xb5 - 0x75)
	computedOffset := tournamentCodeOffset(custom)
	if computedOffset != expectedOffset {
		t.Fatalf("code offset for long name = %#x, want %#x", computedOffset, expectedOffset)
	}

	const code = "987654321098"
	got := stampTournament(custom, 5555, make([]byte, 8), code)
	if !tournamentCodeMatches(got, code) {
		t.Fatal("code match failed for variable length name")
	}
}

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
