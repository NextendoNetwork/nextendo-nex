package nex

// Relecture Shared Content (mineur du coeur) : ReadList / ReadMap ne reservent plus d'un coup
// le compte annonce (borne seulement par les octets restants) et s'arretent a la premiere
// erreur.

import (
	"encoding/binary"
	"testing"
)

func TestReadListNeReservePasLeCompteAnnonce(t *testing.T) {
	s := testSettings()
	corps := make([]byte, 1<<20)

	// 1 Mio annonce autant d'elements que d'octets ; le premier element echoue.
	binary.LittleEndian.PutUint32(corps, uint32(len(corps)-4))
	in := NewStreamIn(corps, s)
	l2 := ReadList(in, func(x *StreamIn) [5]uint64 {
		var v [5]uint64
		v[0] = x.U64()
		_ = x.Read(1 << 21) // erreur : plus que le reste
		return v
	})
	if in.Err() == nil {
		t.Fatal("erreur attendue")
	}
	if cap(l2) > 256 || len(l2) != 1 {
		t.Fatalf("ReadList : len=%d cap=%d (au plus 256 reserves, arret a la premiere erreur)", len(l2), cap(l2))
	}

	in = NewStreamIn(corps, s)
	m := ReadMap(in, func(x *StreamIn) uint64 { return x.U64() }, func(x *StreamIn) []byte { return x.Read(1 << 21) })
	if in.Err() == nil || len(m) > 1 {
		t.Fatalf("ReadMap : err=%v len=%d", in.Err(), len(m))
	}
}
