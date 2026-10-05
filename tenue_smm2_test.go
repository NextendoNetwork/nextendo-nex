package nex

import "testing"

// TestSyncUserProfileGardeLaTenue : la 49 porte le profil a jour — pseudo, les quatre u16
// de la tenue, le Mii — dans la forme de RegisterUserParam (mesure chez Nintendo le
// 2026-10-05). La tenue doit etre gardee, sinon elle disparait au rechargement.
func TestSyncUserProfileGardeLaTenue(t *testing.T) {
	s := testSettings()
	conn := &Connection{Settings: s, PID: 4242}
	smm2Profils.Store(conn.PID, SMM2Profil{PID: conn.PID, Nom: "Avant", Mii: []byte{1}})
	defer smm2Profils.Delete(conn.PID)

	corps := NewStreamOut(s)
	corps.String("Apres")
	tenue := NewStreamOut(s)
	for _, v := range []uint16{0, 12, 38, 1} {
		tenue.U16(v)
	}
	corps.U8(0)
	corps.Buffer(tenue.Bytes())
	corps.QBuffer([]byte{9, 9, 9})
	out := NewStreamOut(s)
	out.U8(1)
	out.Buffer(corps.Bytes())

	r := handleDataStoreSyncUserProfile(conn, &RMCMessage{Settings: s, Method: 49, CallID: 1, Body: out.Bytes()})
	if r.IsError {
		t.Fatal("49 en erreur")
	}
	p, _ := SMM2ProfilDe(conn.PID)
	if p.Unk != [4]uint16{0, 12, 38, 1} || p.Nom != "Apres" || len(p.Mii) != 3 {
		t.Fatalf("profil apres 49 : %+v", p)
	}
	// Sans corps, rien ne change.
	handleDataStoreSyncUserProfile(conn, &RMCMessage{Settings: s, Method: 49, CallID: 2})
	if p2, _ := SMM2ProfilDe(conn.PID); p2.Unk != p.Unk {
		t.Fatal("une 49 sans corps a efface la tenue")
	}
}
