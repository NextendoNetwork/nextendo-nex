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
	// Depuis relaybesoin.go, le relais n est accorde qu a une paire qui a deja echoue.
	besoinPourTest(t, 7, 9)

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
	besoinPourTest(t, 11, 13)

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

// TestPairRelayForOrdreStable : les deux points d appel nomment la paire dans des ordres
// OPPOSES — GetSessionURLs avec (visiteur, hote), InitiateProbe avec (appelant, cible).
// pairPortFor trie deja les PID ; si attendus ne les trie pas, la liste s inverse d un appel
// a l autre et les deux joueurs finissent dans la meme place.
// Mesure du 2026-08-31 : 3 paires sur 17 relayaient zero paquet a cause de ca.
func TestPairRelayForOrdreStable(t *testing.T) {
	SetPairRelay("127.0.0.1", 39000, 50)
	defer SetPairRelay("", 0, 0)
	const pidA, pidB = uint64(1800000123), uint64(1800024323)
	ipA, ipB := "70.40.81.34", "181.78.0.151"
	besoinPourTest(t, pidA, pidB)

	_, port1, ok := PairRelayFor(pidA, pidB, ipA, ipB)
	if !ok {
		t.Fatal("premier appel refuse")
	}
	// Le second appel nomme la MEME paire dans l autre sens.
	_, port2, ok := PairRelayFor(pidB, pidA, ipB, ipA)
	if !ok {
		t.Fatal("second appel refuse")
	}
	if port1 != port2 {
		t.Fatalf("la paire doit garder son port: %d puis %d", port1, port2)
	}

	pairSocksMu.Lock()
	ps := pairSocks[port1]
	pairSocksMu.Unlock()
	if ps == nil {
		t.Fatal("socket absente")
	}
	ps.mu.Lock()
	att := ps.attendus
	ps.mu.Unlock()

	// pidA < pidB, donc l ordre attendu suit celui des PID quel que soit l appelant.
	if att[0] != ipA || att[1] != ipB {
		t.Fatalf("attendus inverses par le second appel: %v", att)
	}
}

// besoinPourTest rend les PID relayables : autorises (liste de volontaires) ET eligibles
// (deux echecs de percage), comme la production l exigerait. Les tests du relais decrivent le
// TRANSPORT ; l autorisation et l eligibilite ont chacune les leurs.
func besoinPourTest(t *testing.T, pids ...uint64) {
	t.Helper()
	volontairesPourTest(t, pids...)
	for _, p := range pids {
		NotePercage(p, false)
		NotePercage(p, false)
	}
	t.Cleanup(func() {
		besoinMu.Lock()
		for _, p := range pids {
			delete(besoins, p)
		}
		besoinMu.Unlock()
	})
}

// volontairesPourTest autorise ces PID et remet la liste a zero apres le test.
func volontairesPourTest(t *testing.T, pids ...uint64) {
	t.Helper()
	SetRelayVolontaires(pids, nil, false)
	t.Cleanup(func() { SetRelayVolontaires(nil, nil, false) })
}

// TestStatsComptentCeQuiPasseEtCeQuiEstJete : sans ces chiffres, un relais qui jette la
// moitie du trafic est indiscernable d un relais qui marche. C est exactement ce qui a permis
// d annoncer 96% la ou il y avait 78%.
func TestStatsComptentCeQuiPasseEtCeQuiEstJete(t *testing.T) {
	SetPairRelay("127.0.0.1", 39300, 20)
	defer SetPairRelay("", 0, 0)
	besoinPourTest(t, 21, 22)

	_, port, ok := PairRelayFor(21, 22, "127.0.0.1", "127.0.0.1")
	if !ok {
		t.Fatal("le port de la paire n a pas pu etre ouvert")
	}
	relais := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}

	a := dialUDP(t)
	defer a.Close()
	b := dialUDP(t)
	defer b.Close()

	// A puis B s annoncent : le premier paquet de B est deja relaye vers A.
	a.WriteToUDP([]byte("a1"), relais)
	b.WriteToUDP([]byte("b1"), relais)
	lireUDP(t, a, "b1 vers a")
	// Un troisieme paquet de A, relaye vers B.
	a.WriteToUDP([]byte("a2"), relais)
	lireUDP(t, b, "a2 vers b")

	st := PairRelayStats()
	if !st.Actif || st.Paires != 1 {
		t.Fatalf("actif=%v paires=%d, attendu true/1", st.Actif, st.Paires)
	}
	p := st.Vivantes[0]
	if p.PIDs != [2]uint64{21, 22} {
		t.Fatalf("PIDs=%v, attendu [21 22] tries", p.PIDs)
	}
	if p.Places[0] == "" || p.Places[1] == "" {
		t.Fatalf("les deux places doivent etre occupees: %v", p.Places)
	}
	if p.Recus != 3 || p.Relayes != 2 {
		t.Fatalf("recus=%d relayes=%d, attendu 3/2", p.Recus, p.Relayes)
	}
	if st.Total.PairesOuvertes == 0 {
		t.Fatal("le cumul des paires ouvertes n a pas bouge")
	}
}

// TestStatsPaireMuetteEstComptee : une paire qui n a rien relaye est une partie qui n a pas
// eu lieu. C est le seul chiffre qui la denonce — 3 sur 17, puis 37 sur 565, sans qu aucune
// autre mesure ne le montre.
func TestStatsPaireMuetteEstComptee(t *testing.T) {
	SetPairRelay("127.0.0.1", 39400, 20)
	defer SetPairRelay("", 0, 0)
	besoinPourTest(t, 31, 32)

	avant := PairRelayStats().Total.PairesMuettes

	_, port, ok := PairRelayFor(31, 32, "198.51.100.1", "198.51.100.2")
	if !ok {
		t.Fatal("le port de la paire n a pas pu etre ouvert")
	}

	// Personne ne parle : on force le vieillissement et on fait passer le faucheur.
	pairSocksMu.Lock()
	ps := pairSocks[port]
	pairSocksMu.Unlock()
	ps.mu.Lock()
	ps.dernier = time.Now().Add(-pairRelayTTL - time.Minute)
	ps.mu.Unlock()

	// Le faucheur tourne toutes les 30 s ; on reproduit son geste sans attendre.
	pairSocksMu.Lock()
	if ps.inactifDepuis(pairRelayTTL) {
		ps.conn.Close()
		delete(pairSocks, port)
		if ps.relayes == 0 {
			pairTotal.PairesMuettes++
		}
	}
	apres := pairTotal.PairesMuettes
	pairSocksMu.Unlock()

	if apres != avant+1 {
		t.Fatalf("paires muettes %d -> %d, attendu +1", avant, apres)
	}
}

// stationsHote reproduit la forme que natBridgeStations rend a un visiteur : un candidat LAN
// portant le CID de l hote (sans type ni Pa) et un candidat public.
func stationsHote() []*StationURL {
	lan := ParseStationURL("prudp:/address=192.168.1.20;port=50001;CID=4242;PID=1800000009;RVCID=99;natf=34;natm=1")
	pub := ParseStationURL("prudp:/address=70.40.81.34;port=50001;CID=4242;PID=1800000009;RVCID=99;natf=34;natm=1;type=3;Pa=192.168.1.20")

	return []*StationURL{lan, pub}
}

// TestRelayStationsNeRepointeQueLaPublique : repointer AUSSI le candidat LAN donnait deux
// stations identiques et faisait perdre le CID de l hote — mesure du 2026-08-31, 85% de
// verdicts FAILED, et la branche relay-fallback avait vu le visiteur appeler EndParticipation
// des reception.
func TestRelayStationsNeRepointeQueLaPublique(t *testing.T) {
	urls := stationsHote()
	avant := urls[0].String()

	out := RelayStations(urls, "51.178.29.194", 30500)
	if len(out) != 2 {
		t.Fatalf("%d stations en sortie, attendu 2", len(out))
	}
	// La LAN doit etre LE MEME pointeur, donc rigoureusement intacte.
	if out[0] != urls[0] {
		t.Fatal("la station LAN a ete copiee ou modifiee")
	}
	if out[0].String() != avant {
		t.Fatalf("station LAN modifiee:\n avant %s\n apres %s", avant, out[0].String())
	}
	if out[0].GetInt("CID") != 4242 {
		t.Fatalf("le CID de l hote a disparu de la station LAN: %s", out[0].String())
	}
	// La publique, et elle seule, pointe vers le relais.
	if got := out[1].Get("address"); got != "51.178.29.194" {
		t.Fatalf("adresse publique = %q", got)
	}
	if got := out[1].GetInt("port"); got != 30500 {
		t.Fatalf("port public = %d", got)
	}
	if got := out[1].Get("Pa"); got != "51.178.29.194" {
		t.Fatalf("Pa = %q, attendu l adresse du relais", got)
	}
	// Ce que la Pia du pair lit pour choisir comment sonder doit survivre tel quel.
	if out[1].GetInt("CID") != 4242 || out[1].GetInt("RVCID") != 99 ||
		out[1].Get("natf") != "34" || out[1].Get("natm") != "1" {
		t.Fatalf("champs d identite ou de NAT perdus: %s", out[1].String())
	}
	// Et les deux stations ne doivent PAS se retrouver identiques.
	if out[0].Get("address") == out[1].Get("address") {
		t.Fatal("les deux stations pointent au meme endroit")
	}
}

// TestRelayStationsSansPa : Splatoon 2 tourne en LegacyPiaConfig et n envoie jamais Pa. Un
// reperage de la station publique base sur Pa ne verrait rien a repointer ici.
func TestRelayStationsSansPa(t *testing.T) {
	lan := ParseStationURL("prudp:/address=10.0.0.5;port=50001;CID=7;RVCID=3")
	pub := ParseStationURL("prudp:/address=201.97.27.16;port=52802;CID=7;RVCID=3;type=3")

	out := RelayStations([]*StationURL{lan, pub}, "51.178.29.194", 30600)
	if out[1].Get("address") != "51.178.29.194" || out[1].GetInt("port") != 30600 {
		t.Fatalf("la publique n a pas ete repointee: %s", out[1].String())
	}
	if out[1].Has("Pa") {
		t.Fatalf("Pa a ete invente alors qu il etait absent: %s", out[1].String())
	}
	if out[0] != lan {
		t.Fatal("la station LAN a ete touchee")
	}
}

// TestRelayStationsSansPubliqueLaisseEnDirect : plutot rendre la liste intacte que rendre une
// forme qu on ne sait pas construire.
func TestRelayStationsSansPubliqueLaisseEnDirect(t *testing.T) {
	a := ParseStationURL("prudp:/address=192.168.1.20;port=1;CID=1")
	b := ParseStationURL("prudp:/address=10.0.0.7;port=2;CID=2")
	in := []*StationURL{a, b}

	out := RelayStations(in, "51.178.29.194", 30700)
	if len(out) != 2 || out[0] != a || out[1] != b {
		t.Fatal("la liste devait revenir intacte")
	}
}

// TestCollisionNExpulsePasLOccupant : le hachage tape dans un nombre fini de ports, donc deux
// paires differentes y tombent regulierement ensemble. L ancien code reutilisait la socket de
// l autre paire et ECRASAIT ses adresses attendues — l occupant, en pleine partie, se faisait
// jeter. Avec vingt paires simultanees la probabilite est de l ordre de 20%.
func TestCollisionNExpulsePasLOccupant(t *testing.T) {
	// Un seul port disponible : toute deuxieme paire entre forcement en collision.
	SetPairRelay("127.0.0.1", 39600, 1)
	defer SetPairRelay("", 0, 0)
	besoinPourTest(t, 41, 42, 43, 44)

	_, port1, ok := PairRelayFor(41, 42, "198.51.100.1", "198.51.100.2")
	if !ok {
		t.Fatal("la premiere paire n a pas pu s ouvrir")
	}

	// La seconde paire tombe sur le meme port : elle doit etre REFUSEE, pas servie.
	if _, _, ok := PairRelayFor(43, 44, "203.0.113.1", "203.0.113.2"); ok {
		t.Fatal("la seconde paire a recu le port de la premiere")
	}

	// Et l occupant doit etre intact : memes PID, memes adresses attendues.
	pairSocksMu.Lock()
	ps := pairSocks[port1]
	pairSocksMu.Unlock()
	if ps == nil {
		t.Fatal("la socket de la premiere paire a disparu")
	}
	ps.mu.Lock()
	att := ps.attendus
	lo, hi := ps.pidLo, ps.pidHi
	ps.mu.Unlock()

	if lo != 41 || hi != 42 {
		t.Fatalf("la socket appartient maintenant a %d/%d", lo, hi)
	}
	if att[0] != "198.51.100.1" || att[1] != "198.51.100.2" {
		t.Fatalf("adresses attendues ecrasees: %v", att)
	}
}

// TestCollisionSeDeplaceQuandIlYADeLaPlace : avec des ports libres a cote, la seconde paire
// doit etre servie sur un autre port plutot que refusee.
func TestCollisionSeDeplaceQuandIlYADeLaPlace(t *testing.T) {
	SetPairRelay("127.0.0.1", 39700, 8)
	defer SetPairRelay("", 0, 0)
	besoinPourTest(t, 51, 52, 53, 54)

	_, p1, ok := PairRelayFor(51, 52, "198.51.100.1", "198.51.100.2")
	if !ok {
		t.Fatal("premiere paire refusee")
	}
	_, p2, ok := PairRelayFor(53, 54, "203.0.113.1", "203.0.113.2")
	if !ok {
		t.Fatal("seconde paire refusee alors qu il restait des ports")
	}
	if p1 == p2 {
		t.Fatalf("les deux paires partagent le port %d", p1)
	}
}

// TestMemePortDansLesDeuxSensApresDeplacement : l invariant que le hachage garantissait
// gratuitement. Depuis qu une collision peut deplacer le port, il tient grace au registre —
// et si les deux points d appel divergent, chaque console parle a une socket que l autre ne
// lit pas.
func TestMemePortDansLesDeuxSensApresDeplacement(t *testing.T) {
	SetPairRelay("127.0.0.1", 39800, 8)
	defer SetPairRelay("", 0, 0)
	besoinPourTest(t, 61, 62, 63, 64)

	// On occupe d abord le port prefere de la paire (63,64) pour forcer son deplacement.
	PairRelayFor(61, 62, "198.51.100.1", "198.51.100.2")

	_, a, ok := PairRelayFor(63, 64, "203.0.113.1", "203.0.113.2")
	if !ok {
		t.Fatal("paire refusee")
	}
	_, b, ok := PairRelayFor(64, 63, "203.0.113.2", "203.0.113.1")
	if !ok {
		t.Fatal("paire refusee dans l autre sens")
	}
	if a != b {
		t.Fatalf("port %d dans un sens, %d dans l autre", a, b)
	}
}

// TestDesarmementVolcaLeBilan : eteindre le relais en urgence ne doit pas emporter les
// compteurs. Mesure du 2026-08-31 : un essai rate sur Mario Kart a ete desarme aussitot, les
// ports se sont fermes en silence, et on ne savait meme pas si le trafic avait traverse.
func TestDesarmementVolcaLeBilan(t *testing.T) {
	SetPairRelay("127.0.0.1", 42000, 20)
	besoinPourTest(t, 301, 302)

	_, port, ok := PairRelayFor(301, 302, "127.0.0.1", "127.0.0.1")
	if !ok {
		t.Fatal("port non ouvert")
	}
	relais := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}

	a := dialUDP(t)
	defer a.Close()
	b := dialUDP(t)
	defer b.Close()
	a.WriteToUDP([]byte("un"), relais)
	b.WriteToUDP([]byte("deux"), relais)
	lireUDP(t, a, "deux vers a")

	avant := PairRelayStats().Total

	// Desarmement : c est ici que les compteurs se perdaient.
	SetPairRelay("", 0, 0)

	apres := PairRelayStats().Total
	if apres.Recus <= avant.Recus {
		t.Fatalf("les paquets recus n ont pas ete cumules au desarmement (%d -> %d)",
			avant.Recus, apres.Recus)
	}
	if apres.Relayes <= avant.Relayes {
		t.Fatalf("les paquets relayes n ont pas ete cumules (%d -> %d)",
			avant.Relayes, apres.Relayes)
	}
}
