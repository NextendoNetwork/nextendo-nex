package nex

import (
	"bytes"
	"testing"
)

// TestDoublonFiableNonRetraite : un paquet fiable renvoye par la console (notre ACK s'est
// perdu) est reconfirme mais PAS retraite. Avant, il etait ajoute une seconde fois au
// reassemblage — et, en fin de requete, la requete etait traitee deux fois : deux
// reponses en parallele dont les fragments s'entrelacaient (blocage du mode sans fin de
// SMM2, 12 cas sur 12 en production).
func TestDoublonFiableNonRetraite(t *testing.T) {
	acks := 0
	c := NewConnection(&Endpoint{Settings: testSettings()}, "test", func([]byte) { acks++ })
	frag := func(seq uint16, fragID uint8, charge string) *Packet {
		return &Packet{Type: PacketDATA, Flags: FlagNeedACK | FlagReliable, PacketID: seq, FragmentID: fragID, Payload: []byte(charge)}
	}

	c.processData(frag(5, 1, "AAA"))
	c.processData(frag(5, 1, "AAA")) // le renvoi
	c.processData(frag(6, 2, "BBB"))

	if !bytes.Equal(c.fragBuf, []byte("AAABBB")) {
		t.Fatalf("reassemblage %q, attendu %q", c.fragBuf, "AAABBB")
	}
	if acks != 3 {
		t.Fatalf("%d ACK envoye(s), attendu 3 : le renvoi doit etre reconfirme", acks)
	}

	// Une nouvelle session (SYN) repart de un : un numero deja vu redevient neuf.
	c.processSYN(&Packet{Type: PacketSYN})
	c.fragBuf = nil
	c.processData(frag(5, 1, "CCC"))
	if !bytes.Equal(c.fragBuf, []byte("CCC")) {
		t.Fatalf("apres SYN : %q, attendu %q", c.fragBuf, "CCC")
	}
}
