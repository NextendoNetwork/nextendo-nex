package nex

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Forme de reponse deduite de l'observation du protocole 0x70/0x16 : le client demande N
// 2026-08-12). Le client demande les profils de neuf joueurs ; six répondent
// présents, trois entrées reviennent vides.

// Les neuf PID de la demande, dans l'ordre.
var capturedPIDs = []string{
	"419256a1cfd081c4", // 0 — pas de profil chez Nintendo
	"5cf3e17a20566137", // 1 — Lucas
	"665e5580b4eb1268", // 2 — yakibou
	"bd5cfc6bfdf05056", // 3 — no name
	"aa06cec732149492", // 4 — kaeloo
	"468ac24ebb6aaee4", // 5 — pas de profil
	"67c05ef9597ad531", // 6 — Iyovi
	"253f7199c90a365e", // 7 — Papa
	"1e28ae45a8cd4bad", // 8 — pas de profil
}

func pidFromHex(t *testing.T, s string) uint64 {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		t.Fatalf("pid invalide %q", s)
	}
	var v uint64
	for i := 7; i >= 0; i-- { // little-endian, comme sur le fil
		v = v<<8 | uint64(b[i])
	}
	return v
}

// TestUploadCommonDataStoresBlob : le dépôt doit être retenu, pas jeté.
func TestUploadCommonDataStoresBlob(t *testing.T) {
	s := testSettings()
	conn := &Connection{Settings: s, PID: 1001}

	blob := make([]byte, 132)
	copy(blob, []byte{0x52, 0x46, 0x00, 0x00})
	copy(blob[0x14:], []byte("M\x00o\x00h\x00a\x00"))

	body := NewStreamOut(s)
	body.Buffer(blob)
	body.U64(0) // le u64 de queue vu sur la measured

	resp := RankingHandler()(conn, &RMCMessage{
		Protocol: ProtocolRanking, Method: MethodUploadCommonData, CallID: 1, Body: body.Bytes(),
	})
	if resp == nil {
		t.Fatal("pas de réponse au dépôt")
	}
	got := CommonData(conn.PID)
	if !bytes.Equal(got, blob) {
		t.Fatalf("profil non retenu : %x", got)
	}
	ForgetCommonData(conn.PID)
}

// TestCommonDataByPIDsShape vérifie la forme exacte de la réponse contre celle
// de Nintendo : enveloppe, nombre d'entrées, ordre, et entrées vides pour les
// joueurs inconnus.
func TestCommonDataByPIDsShape(t *testing.T) {
	s := testSettings()
	conn := &Connection{Settings: s, PID: 1001}

	// Six profils sur neuf, aux mêmes positions que dans la measured.
	avecProfil := map[int]bool{1: true, 2: true, 3: true, 4: true, 6: true, 7: true}
	blob := make([]byte, 132)
	copy(blob, []byte{0x52, 0x46, 0x00, 0x00})
	for i, ph := range capturedPIDs {
		if avecProfil[i] {
			PutCommonData(pidFromHex(t, ph), blob)
		}
	}
	defer func() {
		for _, ph := range capturedPIDs {
			ForgetCommonData(pidFromHex(t, ph))
		}
	}()

	req := NewStreamOut(s)
	req.U32(uint32(len(capturedPIDs)))
	for _, ph := range capturedPIDs {
		req.PID(pidFromHex(t, ph))
	}

	resp := RankingHandler()(conn, &RMCMessage{
		Protocol: ProtocolRanking, Method: methodRankingCommonDataByPIDs, CallID: 2, Body: req.Bytes(),
	})
	if resp == nil {
		t.Fatal("pas de réponse")
	}
	b := resp.Body

	// Enveloppe : u8 version, u32 taille du contenu.
	if b[0] != 0 {
		t.Errorf("version = %d, attendu 0", b[0])
	}
	size := uint32(b[1]) | uint32(b[2])<<8 | uint32(b[3])<<16 | uint32(b[4])<<24
	if int(size) != len(b)-5 {
		t.Errorf("taille annoncée %d, contenu réel %d", size, len(b)-5)
	}
	count := uint32(b[5]) | uint32(b[6])<<8 | uint32(b[7])<<16 | uint32(b[8])<<24
	if count != 9 {
		t.Errorf("nombre d'entrées = %d, attendu 9", count)
	}

	// Parcours des neuf qBuffer : longueurs attendues et fin exacte du tampon.
	off := 9
	for i := 0; i < 9; i++ {
		if off+2 > len(b) {
			t.Fatalf("entrée %d tronquée", i)
		}
		ln := int(b[off]) | int(b[off+1])<<8
		want := 0
		if avecProfil[i] {
			want = 132
		}
		if ln != want {
			t.Errorf("entrée %d : longueur %d, attendu %d", i, ln, want)
		}
		off += 2 + ln
	}
	if off != len(b) {
		t.Errorf("fin à %d alors que le tampon fait %d", off, len(b))
	}

	// La measured de Nintendo fait 819 octets pour ces mêmes six profils de 132.
	if len(b) != 819 {
		t.Errorf("réponse de %d octets, Nintendo en produit 819", len(b))
	}
}

// TestCommonDataByPIDsRejectsAbsurdCount : une demande forgée ne doit pas nous
// faire produire une réponse arbitrairement grande.
func TestCommonDataByPIDsRejectsAbsurdCount(t *testing.T) {
	s := testSettings()
	conn := &Connection{Settings: s, PID: 1001}

	req := NewStreamOut(s)
	req.U32(1 << 20) // un million d'entrées annoncées

	resp := RankingHandler()(conn, &RMCMessage{
		Protocol: ProtocolRanking, Method: methodRankingCommonDataByPIDs, CallID: 3, Body: req.Bytes(),
	})
	if resp == nil {
		t.Fatal("pas de réponse")
	}
	if len(resp.Body) > 64 {
		t.Errorf("une demande absurde a produit %d octets", len(resp.Body))
	}
}
