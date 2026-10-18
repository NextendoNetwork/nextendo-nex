package nex

import (
	"bytes"
	"sync/atomic"
	"testing"
)

// connDoublon : une connexion (port non secure) qui compte les appels du gestionnaire 0x6D
// et les ACK renvoyes.
func connDoublon(t *testing.T) (*Connection, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	s := testSettings()
	ep := NewEndpoint(s)
	var appels, acks atomic.Int32
	ep.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
		appels.Add(1)

		return nil
	})
	conn := NewConnection(ep, "192.0.2.61:1261", func(b []byte) {
		if p, _, err := DecodePackets(b); err == nil && len(p) == 1 && p[0].Type == PacketDATA && p[0].HasFlag(FlagACK) {
			acks.Add(1)
		}
	})
	conn.state.Store(stateConnected)

	return conn, &appels, &acks
}

// Une DATA reemise (meme numero de sequence) est acquittee de nouveau mais servie une seule
// fois. Avant : le gestionnaire etait appele deux fois (prod : AutoMatchmake en double, second
// salon fantome).
func TestDataReemiseServieUneFois(t *testing.T) {
	conn, appels, acks := connDoublon(t)
	req := NewRMCRequest(conn.Settings, 0x6D, 40, 25, []byte{1, 2, 3}).Encode()
	conn.Feed(EncodePacket(dataPacket(req, 12)))
	conn.Feed(EncodePacket(dataPacket(req, 12)))
	if n := appels.Load(); n != 1 {
		t.Fatalf("gestionnaire appele %d fois, attendu 1", n)
	}
	if n := acks.Load(); n != 2 {
		t.Fatalf("%d ACK, attendu 2 (la reemission est acquittee aussi)", n)
	}
	// La requete suivante, nouveau numero, est servie.
	conn.Feed(EncodePacket(dataPacket(NewRMCRequest(conn.Settings, 0x6D, 40, 26, nil).Encode(), 13)))
	if n := appels.Load(); n != 2 {
		t.Fatalf("requete suivante : %d appels, attendu 2", n)
	}
}

// Fragments reemis : f1, f0, f0 donnait un « size mismatch » (le f0 reemis reassemble seul) ;
// f1, f1, f0 perdait la requete (taille doublee). Les deux sont servis une fois, et la
// requete d'apres aussi.
func TestFragmentsReemisReassemblesUneFois(t *testing.T) {
	for _, cas := range []struct {
		nom   string
		ordre []int // indices dans [f1, f0]
	}{
		{"f1 f0 f0", []int{0, 1, 1}},
		{"f1 f1 f0", []int{0, 0, 1}},
	} {
		t.Run(cas.nom, func(t *testing.T) {
			conn, appels, _ := connDoublon(t)
			// Le dernier fragment est a lui seul une requete valide : servi seul, il
			// appellerait le gestionnaire une seconde fois (avant ce correctif).
			fin := NewRMCRequest(conn.Settings, 0x6D, 1, 99, nil).Encode()
			req := NewRMCRequest(conn.Settings, 0x6D, 1, 7, append(bytes.Repeat([]byte("texte ASCII "), 40), fin...)).Encode()
			coupe := len(req) - len(fin)
			frags := []*Packet{fragmentData(req[:coupe], 30, 1), fragmentData(req[coupe:], 31, 0)}
			for _, i := range cas.ordre {
				conn.Feed(EncodePacket(frags[i]))
			}
			if n := appels.Load(); n != 1 {
				t.Fatalf("gestionnaire appele %d fois, attendu 1", n)
			}
			if len(conn.fragBuf) != 0 {
				t.Fatalf("tampon de reassemblage non vide : %d octets", len(conn.fragBuf))
			}
			conn.Feed(EncodePacket(dataPacket(NewRMCRequest(conn.Settings, 0x6D, 1, 8, nil).Encode(), 32)))
			if n := appels.Load(); n != 2 {
				t.Fatalf("requete suivante : %d appels, attendu 2", n)
			}
		})
	}
}

// Les numeros qui partagent une case (meme PacketID%256) restent distincts, et une DATA non
// fiable n'est jamais dedoublonnee (comportement d'avant).
func TestDoublonsCasesEtNonFiables(t *testing.T) {
	conn, appels, _ := connDoublon(t)
	req := NewRMCRequest(conn.Settings, 0x6D, 1, 1, nil).Encode()
	for _, id := range []uint16{3, 259, 515, 3 + 768} {
		conn.Feed(EncodePacket(dataPacket(req, id)))
	}
	if n := appels.Load(); n != 4 {
		t.Fatalf("%d appels pour 4 numeros distincts de la meme case, attendu 4", n)
	}
	nonFiable := dataPacket(req, 900)
	nonFiable.Flags &^= FlagReliable
	conn.Feed(EncodePacket(nonFiable))
	conn.Feed(EncodePacket(nonFiable))
	if n := appels.Load(); n != 6 {
		t.Fatalf("DATA non fiable : %d appels, attendu 6", n)
	}
}
