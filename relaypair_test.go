package nex

import (
	"net"
	"testing"
	"time"
)

// Le port d une paire doit etre le MEME quel que soit l ordre des deux PID : le chemin des
// URL de session et celui de l initiation de sonde le calculent chacun de leur cote, sans
// rien se transmettre, et doivent tomber sur le meme port. Une asymetrie ferait emettre les
// deux consoles sur deux ports differents, et le relais ne verrait jamais que des moities.
func TestPairPortSymetriqueEtBorne(t *testing.T) {
	SetPairRelay("203.0.113.10", 31000, 100)
	defer SetPairRelay("", 0, 0)

	for _, c := range [][2]uint64{{1, 2}, {1800000001, 9999}, {42, 42}} {
		a, okA := pairPortFor(c[0], c[1])
		b, okB := pairPortFor(c[1], c[0])
		if !okA || !okB {
			t.Fatalf("paire %v: attribution refusee alors que le relais est arme", c)
		}
		if a != b {
			t.Errorf("paire %v: %d dans un sens, %d dans l autre — le port doit etre symetrique", c, a, b)
		}
		if a < 31000 || a >= 31100 {
			t.Errorf("paire %v: port %d hors de la plage 31000-31099", c, a)
		}
	}
}

// Relais desarme : aucune attribution, donc aucune substitution possible.
func TestPairPortInerteQuandDesarme(t *testing.T) {
	SetPairRelay("", 0, 0)
	if _, ok := pairPortFor(1, 2); ok {
		t.Fatal("le relais desarme ne doit attribuer aucun port")
	}
	if PairRelayActive() {
		t.Fatal("PairRelayActive doit etre faux quand le relais est desarme")
	}
}

// Le coeur du mecanisme : ce qui arrive d une extremite repart vers l autre, sans que rien
// ne lise le contenu. C est ce qui permet a la sonde de perçage de 16 octets — qui ne porte
// aucun identifiant — de traverser, la ou un relais a port unique ne pouvait que lui
// repondre lui-meme, ce que Pia refuse.
func TestPaireFaitTraverserDansLesDeuxSens(t *testing.T) {
	SetPairRelay("127.0.0.1", 31500, 50)
	defer SetPairRelay("", 0, 0)

	_, port, ok := PairRelayFor(7, 9, "127.0.0.1", "127.0.0.1")
	if !ok {
		t.Fatal("le port de la paire n a pas pu etre ouvert")
	}
	relais := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}

	a := dialUDP(t)
	defer a.Close()
	b := dialUDP(t)
	defer b.Close()

	// Chaque extremite doit s annoncer une fois pour etre apprise.
	sonde := []byte{0x00, 0x00, 0x00, 0x65, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := a.WriteToUDP(sonde, relais); err != nil {
		t.Fatalf("A -> relais: %v", err)
	}
	if _, err := b.WriteToUDP(sonde, relais); err != nil {
		t.Fatalf("B -> relais: %v", err)
	}
	// Le premier paquet de B est deja reexpedie vers A : on le consomme.
	lireUDP(t, a, "premier paquet de B vers A")

	// A -> B
	if _, err := a.WriteToUDP([]byte("bonjour-B"), relais); err != nil {
		t.Fatalf("A -> relais: %v", err)
	}
	if got := lireUDP(t, b, "A vers B"); got != "bonjour-B" {
		t.Errorf("B a reçu %q, attendu \"bonjour-B\"", got)
	}

	// B -> A
	if _, err := b.WriteToUDP([]byte("bonjour-A"), relais); err != nil {
		t.Fatalf("B -> relais: %v", err)
	}
	if got := lireUDP(t, a, "B vers A"); got != "bonjour-A" {
		t.Errorf("A a reçu %q, attendu \"bonjour-A\"", got)
	}
}

// Un tiers qui tombe sur le port ne doit RIEN pouvoir injecter : sans cette garde, un
// scanner s inserant dans l appairage detournerait la partie de deux joueurs.
func TestPaireRefuseUneAdresseNonAttendue(t *testing.T) {
	SetPairRelay("127.0.0.1", 31600, 50)
	defer SetPairRelay("", 0, 0)

	_, port, ok := PairRelayFor(11, 13, "198.51.100.7", "198.51.100.8")
	if !ok {
		t.Fatal("le port de la paire n a pas pu etre ouvert")
	}
	relais := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}

	intrus := dialUDP(t)
	defer intrus.Close()
	if _, err := intrus.WriteToUDP([]byte("injection"), relais); err != nil {
		t.Fatalf("intrus -> relais: %v", err)
	}

	intrus.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := intrus.ReadFromUDP(buf); err == nil {
		t.Fatalf("le relais a repondu %d octets a une adresse non attendue", n)
	}
}

func dialUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("socket de test: %v", err)
	}

	return c
}

func lireUDP(t *testing.T, c *net.UDPConn, quoi string) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("%s: rien reçu (%v)", quoi, err)
	}

	return string(buf[:n])
}
