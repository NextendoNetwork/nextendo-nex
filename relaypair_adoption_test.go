package nex

import (
	"net"
	"testing"
)

var paquetPia = []byte{0x32, 0xab, 0x98, 0x64, 0x84, 0, 0, 0}

// L UDP d un joueur sort par une adresse sans rapport avec sa connexion NEX (prod 17/10,
// ports 22512, 22597, 22513 : relayes=0). Sa place, jamais vue alors que l autre parle,
// est adoptee par la premiere source inconnue qui parle Pia, une seule fois par port ; le
// vrai joueur la reprend s il se presente ensuite depuis son adresse attendue.
func TestAdoptionDUnePlaceJamaisVue(t *testing.T) {
	ps := &pairSocket{port: 30700, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"198.51.100.10", "203.0.113.20"}
	vu := &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 1000}
	ailleurs := &net.UDPAddr{IP: net.ParseIP("192.0.2.50"), Port: 4000}

	// Personne n a encore parle : rien a adopter.
	if dst := ps.apparier(ailleurs, paquetPia); dst != nil || ps.vus[1] != nil {
		t.Fatalf("aucune place vue : pas d adoption, obtenu %v", dst)
	}
	ps.apparier(vu, paquetPia)
	// Pas un paquet Pia : refuse.
	if dst := ps.apparier(ailleurs, []byte("GET / HTTP/1.1")); dst != nil || ps.vus[1] != nil {
		t.Fatalf("paquet non Pia : refus attendu, obtenu %v", dst)
	}
	// Paquet Pia : adopte, relaye vers l autre joueur, et l autre joueur lui est relaye.
	if dst := ps.apparier(ailleurs, paquetPia); dst == nil || dst.Port != 1000 {
		t.Fatalf("adoption : relais vers %v attendu, obtenu %v", vu, dst)
	}
	if dst := ps.apparier(vu, paquetPia); dst == nil || !dst.IP.Equal(ailleurs.IP) {
		t.Fatalf("l autre joueur doit etre relaye vers la source adoptee, obtenu %v", dst)
	}
	// Une seule fois par port : une autre source inconnue reste refusee.
	autre := &net.UDPAddr{IP: net.ParseIP("192.0.2.60"), Port: 4001}
	if dst := ps.apparier(autre, paquetPia); dst != nil {
		t.Fatalf("seconde source inconnue : refus attendu, obtenu %v", dst)
	}
	// Le vrai joueur arrive de son adresse attendue : il reprend la place.
	vrai := &net.UDPAddr{IP: net.ParseIP("203.0.113.20"), Port: 5000}
	if dst := ps.apparier(vrai, paquetPia); dst == nil || dst.Port != 1000 {
		t.Fatalf("le joueur attendu doit reprendre sa place, obtenu %v", dst)
	}
	if dst := ps.apparier(vu, paquetPia); dst == nil || dst.Port != 5000 {
		t.Fatalf("l autre joueur doit repartir vers le joueur attendu, obtenu %v", dst)
	}
	if dst := ps.apparier(ailleurs, paquetPia); dst != nil {
		t.Fatalf("la source adoptee, remplacee, doit etre refusee, obtenu %v", dst)
	}
}

// Deux attendus du meme /24 (meme foyer, CGNAT) : la reprise par remappage est impossible,
// donc pas d adoption non plus.
func TestPasDAdoptionEntreDeuxAttendusDuMemeBloc(t *testing.T) {
	ps := &pairSocket{port: 30701, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"203.0.113.3", "203.0.113.9"}
	ps.apparier(&net.UDPAddr{IP: net.ParseIP("203.0.113.3"), Port: 1000}, paquetPia)
	if dst := ps.apparier(&net.UDPAddr{IP: net.ParseIP("192.0.2.70"), Port: 4000}, paquetPia); dst != nil {
		t.Fatalf("attendus du meme /24 : pas d adoption, obtenu %v", dst)
	}
}
