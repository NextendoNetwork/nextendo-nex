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

// TestApparierRemappePortQuandLeNATChange : un joueur derriere un NAT symetrique voit son
// port source changer en cours de session. L ancien code jetait ces paquets une fois la
// place prise — mesure du 2026-08-31 : 254 paquets recus pour 104 relayes.
func TestApparierRemappePortQuandLeNATChange(t *testing.T) {
	ps := &pairSocket{port: 30500, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"1.1.1.1", "2.2.2.2"}

	a := &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1000}
	b := &net.UDPAddr{IP: net.ParseIP("2.2.2.2"), Port: 2000}

	if dst := ps.apparier(a); dst != nil {
		t.Fatalf("premier paquet: pas encore de pair, obtenu %v", dst)
	}
	if dst := ps.apparier(b); dst == nil || dst.Port != 1000 {
		t.Fatalf("b doit etre relaye vers a, obtenu %v", dst)
	}

	// Meme IP que a, port different : le NAT a remappe.
	a2 := &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1234}
	dst := ps.apparier(a2)
	if dst == nil || dst.Port != 2000 {
		t.Fatalf("apres remap, a2 doit etre relaye vers b, obtenu %v", dst)
	}
	if ps.remaps != 1 {
		t.Fatalf("remaps=%d, attendu 1", ps.remaps)
	}
	// Et b doit desormais repartir vers le NOUVEAU port de a.
	if dst := ps.apparier(b); dst == nil || dst.Port != 1234 {
		t.Fatalf("b doit suivre le nouveau port de a, obtenu %v", dst)
	}
}

// TestApparierNeRemappePasSurIPPartagee : deux joueurs derriere la MEME IP publique (meme
// foyer, CGNAT) n ont que le port pour se distinguer. Suivre le port melangerait les flux.
func TestApparierNeRemappePasSurIPPartagee(t *testing.T) {
	ps := &pairSocket{port: 30501, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"9.9.9.9", "9.9.9.9"}

	a := &net.UDPAddr{IP: net.ParseIP("9.9.9.9"), Port: 1000}
	b := &net.UDPAddr{IP: net.ParseIP("9.9.9.9"), Port: 2000}
	ps.apparier(a)
	ps.apparier(b)

	c := &net.UDPAddr{IP: net.ParseIP("9.9.9.9"), Port: 3000}
	if dst := ps.apparier(c); dst != nil {
		t.Fatalf("un troisieme port sur une IP partagee doit etre refuse, obtenu %v", dst)
	}
	if ps.remaps != 0 {
		t.Fatalf("remaps=%d, attendu 0", ps.remaps)
	}
	if ps.rejets != 1 {
		t.Fatalf("rejets=%d, attendu 1", ps.rejets)
	}
}

// TestApparierRefuseUneIPEtrangere garde le pare-feu d origine.
func TestApparierRefuseUneIPEtrangere(t *testing.T) {
	ps := &pairSocket{port: 30502, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"1.1.1.1", "2.2.2.2"}
	ps.apparier(&net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1000})

	if dst := ps.apparier(&net.UDPAddr{IP: net.ParseIP("6.6.6.6"), Port: 9}); dst != nil {
		t.Fatalf("une IP hors liste doit etre refusee, obtenu %v", dst)
	}
	if ps.rejetVu["6.6.6.6:9"] != 1 {
		t.Fatalf("le refus doit etre trace par source: %v", ps.rejetVu)
	}
}

// TestAdmisMemeBlocCGNAT : le jeu emet son UDP par une autre sortie que la connexion NEX.
// Mesure du 2026-08-31 : attendu 45.186.208.118, recu de 45.186.208.68, 88 paquets jetes.
func TestAdmisMemeBlocCGNAT(t *testing.T) {
	ps := &pairSocket{port: 30600, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"45.186.208.118", "79.117.187.216"}

	cgnat := &net.UDPAddr{IP: net.ParseIP("45.186.208.68"), Port: 62483}
	autre := &net.UDPAddr{IP: net.ParseIP("79.117.187.216"), Port: 5000}

	if dst := ps.apparier(cgnat); dst != nil {
		t.Fatalf("premier paquet: pas encore de pair, obtenu %v", dst)
	}
	if ps.rejets != 0 {
		t.Fatalf("le meme /24 doit etre admis, rejets=%d", ps.rejets)
	}
	if dst := ps.apparier(autre); dst == nil || dst.Port != 62483 {
		t.Fatalf("doit relayer vers la source CGNAT apprise, obtenu %v", dst)
	}
}

// TestPasDeRemapEntreDeuxJoueursDuMemeBloc : si les deux attendus partagent leur /24, ils ne
// sont plus distinguables par reseau et suivre le port melangerait les flux.
func TestPasDeRemapEntreDeuxJoueursDuMemeBloc(t *testing.T) {
	ps := &pairSocket{port: 30601, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"88.1.2.3", "88.1.2.9"}

	ps.apparier(&net.UDPAddr{IP: net.ParseIP("88.1.2.3"), Port: 1000})
	ps.apparier(&net.UDPAddr{IP: net.ParseIP("88.1.2.9"), Port: 2000})

	intrus := &net.UDPAddr{IP: net.ParseIP("88.1.2.77"), Port: 3000}
	if dst := ps.apparier(intrus); dst != nil {
		t.Fatalf("pas de remap quand les deux attendus partagent le /24, obtenu %v", dst)
	}
	if ps.remaps != 0 {
		t.Fatalf("remaps=%d, attendu 0", ps.remaps)
	}
}

// TestRefuseUnAutreReseau : le garde-fou d origine tient toujours.
func TestRefuseUnAutreReseau(t *testing.T) {
	ps := &pairSocket{port: 30602, rejetVu: map[string]uint64{}}
	ps.attendus = [2]string{"1.1.1.1", "2.2.2.2"}
	if dst := ps.apparier(&net.UDPAddr{IP: net.ParseIP("1.1.2.1"), Port: 9}); dst != nil {
		t.Fatalf("un /24 different doit etre refuse, obtenu %v", dst)
	}
	if ps.rejets != 1 {
		t.Fatalf("rejets=%d, attendu 1", ps.rejets)
	}
}
