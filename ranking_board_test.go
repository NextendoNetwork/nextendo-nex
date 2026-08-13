package nex

import (
	"encoding/hex"
	"testing"
	"time"
)

// Dépôt de fin de course réel, relevé sur le serveur de Nintendo (capture du
// 2026-08-12 : un joueur court un tournoi jusqu'au bout). Tournoi 861706,
// catégorie 393, score 10, profil d'un joueur (pseudonymise).
const captureScoreSubmit = "" +
	"009c0000000a260d008901000001000000" +
	"0a000000ff000000000184005246" +
	"0000c08a3ed8e64d4ab4b2aebc0a42b9d644" +
	"4a006f0075006500000000000000000000000000000000000000000800652e00000000" +
	"0000790100240804040302 0c0c0104000403090603090313040 30d0800000 40a" +
	"0108040a00040214000000000050696c6f74653031000000000000000000000000000000000000000000000000000000000000" +
	"519 4cb"

// TestParseScoreSubmission : lire le dépôt réel doit donner le tournoi, la
// catégorie, le score et un profil de 132 octets.
func TestParseScoreSubmission(t *testing.T) {
	body := captureSubmitBytes(t)
	tour, cat, score, profile, ok := parseScoreSubmission(body)
	if !ok {
		t.Fatal("le dépôt capturé devrait être reconnu")
	}
	if tour != 861706 {
		t.Errorf("tournoi = %d, attendu 861706", tour)
	}
	if cat != 393 {
		t.Errorf("catégorie = %d, attendu 393", cat)
	}
	if score != 10 {
		t.Errorf("score = %d, attendu 10", score)
	}
	if len(profile) != scoreProfileLen {
		t.Errorf("profil de %d octets, attendu %d", len(profile), scoreProfileLen)
	}
	if profile[0] != 0x52 || profile[1] != 0x46 {
		t.Errorf("le profil ne commence pas par sa signature : %x", profile[:4])
	}
}

// TestParseScoreSubmissionRejects : une trame qui n'a pas la forme attendue doit
// être ignorée plutôt que de polluer un classement avec des valeurs inventées.
func TestParseScoreSubmissionRejects(t *testing.T) {
	cases := map[string][]byte{
		"vide":       nil,
		"trop court": make([]byte, 40),
		"tournoi nul": func() []byte {
			b := make([]byte, 200)
			b[0x1b], b[0x1c] = scoreProfileLen, 0
			return b
		}(),
	}
	for nom, body := range cases {
		if _, _, _, _, ok := parseScoreSubmission(body); ok {
			t.Errorf("%s : accepté à tort", nom)
		}
	}
}

// TestBoardOrderAndShape : le classement doit trier par score décroissant, poser
// les rangs à partir de 1, et produire des lignes de 164 octets — la taille
// mesurée sur le classement de Nintendo.
func TestBoardOrderAndShape(t *testing.T) {
	const tour, cat = uint32(4242), uint32(393)
	profile := make([]byte, scoreProfileLen)
	profile[0], profile[1] = 0x52, 0x46

	base := time.Now()
	boardsMu.Lock()
	boards[boardKey{tour, cat}] = map[uint64]*boardEntry{
		1: {PID: 1, Score: 1500, Profile: profile, At: base},
		2: {PID: 2, Score: 4335, Profile: profile, At: base},
		3: {PID: 3, Score: 2765, Profile: profile, At: base.Add(-time.Hour)},
	}
	boardsMu.Unlock()

	e := boardFor(tour, cat)
	if len(e) != 3 {
		t.Fatalf("%d entrée(s), attendu 3", len(e))
	}
	if e[0].Score != 4335 || e[1].Score != 2765 || e[2].Score != 1500 {
		t.Errorf("mauvais ordre : %d, %d, %d", e[0].Score, e[1].Score, e[2].Score)
	}

	out := writeBoard(cat, e)
	// u32 nombre de pages + u8 version + u32 longueur + page
	if n := u32le(out, 0); n != 1 {
		t.Fatalf("%d page(s), attendu 1", n)
	}
	pageLen := int(u32le(out, 5))
	if 9+pageLen != len(out) {
		t.Errorf("longueur de page %d incohérente avec %d octets", pageLen, len(out))
	}
	page := out[9:]
	if c := u32le(page, 0); c != cat {
		t.Errorf("catégorie = %d, attendu %d", c, cat)
	}
	if n := u32le(page, 4); n != 3 {
		t.Errorf("%d entrée(s) annoncée(s), attendu 3", n)
	}
	// Chaque ligne fait 164 octets, plus un pied de 24 — la forme mesurée sur
	// les dix pages de la capture. Sans ce pied, le jeu refuse la page entière.
	if got := len(page) - 8; got != 3*scoreEntryLen+24 {
		t.Errorf("%d octets après l'en-tête, attendu %d", got, 3*scoreEntryLen+24)
	}
	foot := page[8+3*scoreEntryLen:]
	if u32le(foot, 0) != 3 {
		t.Errorf("pied : total = %d, attendu 3", u32le(foot, 0))
	}
	if u32le(foot, 4) != 4 {
		t.Errorf("pied : second champ = %d, attendu 4", u32le(foot, 4))
	}
	for i, v := range foot[8:] {
		if v != 0 {
			t.Errorf("pied : octet %d non nul (%#x)", 8+i, v)
			break
		}
	}
	// Rangs 1, 2, 3 dans l'ordre.
	for i := 0; i < 3; i++ {
		o := 8 + i*scoreEntryLen
		if page[o] != 0 || u32le(page, o+1) != scoreEntryBody {
			t.Errorf("entrée %d : en-tête %x", i, page[o:o+5])
		}
		if r := u32le(page, o+5); r != uint32(i+1) {
			t.Errorf("entrée %d : rang %d", i, r)
		}
	}

	boardsMu.Lock()
	delete(boards, boardKey{tour, cat})
	boardsMu.Unlock()
}

// TestRecordScoreKeepsBest : un classement montre la meilleure performance d'un
// joueur, pas la dernière en date.
func TestRecordScoreKeepsBest(t *testing.T) {
	body := captureSubmitBytes(t)
	if !recordScore(777, body) {
		t.Fatal("dépôt refusé")
	}
	// même trame, score abaissé à 5
	low := append([]byte(nil), body...)
	low[0x11], low[0x12], low[0x13], low[0x14] = 5, 0, 0, 0
	recordScore(777, low)

	e := boardFor(861706, 393)
	var mine *boardEntry
	for _, x := range e {
		if x.PID == 777 {
			mine = x
		}
	}
	if mine == nil {
		t.Fatal("joueur absent du classement")
	}
	if mine.Score != 10 {
		t.Errorf("score conservé = %d, attendu le meilleur (10)", mine.Score)
	}

	boardsMu.Lock()
	delete(boards, boardKey{861706, 393})
	boardsMu.Unlock()
}

func u32le(b []byte, o int) uint32 {
	return uint32(b[o]) | uint32(b[o+1])<<8 | uint32(b[o+2])<<16 | uint32(b[o+3])<<24
}

func captureSubmitBytes(t *testing.T) []byte {
	t.Helper()
	clean := ""
	for _, r := range captureScoreSubmit {
		if r != ' ' {
			clean += string(r)
		}
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		t.Fatalf("hex invalide : %v", err)
	}
	return b
}
