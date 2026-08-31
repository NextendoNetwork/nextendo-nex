package nex

import (
	"net"
	"testing"
)

// Le chemin complet du relais, de bout en bout, sans console.
//
// C est ce qui a manque le 2026-08-30 : chaque essai s est fait directement sur des joueurs,
// et les trois se sont termines par des plaintes. Chaque etape ci-dessous correspond a un
// defaut reellement paye cette nuit-la.
//
// Les deux consoles simulees prennent des adresses de boucle sur des RESEAUX differents
// (127.0.0.x et 127.1.0.x). Ce n est pas un detail : apparier desactive volontairement le
// suivi de port quand les deux adresses attendues partagent leur /24 — deux joueurs derriere
// une meme IP publique ne seraient plus distinguables. Un test ou les deux consoles seraient
// dans le meme /24 ne prouverait donc rien du remappage.
func consoleUDP(t *testing.T, ip string) (*net.UDPConn, string) {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip)})
	if err != nil {
		t.Skipf("impossible d ouvrir une socket sur %s: %v", ip, err)
	}
	t.Cleanup(func() { c.Close() })

	return c, ip
}

func TestBoutEnBoutRelaisPaire(t *testing.T) {
	const pidHote, pidVisiteur = uint64(1800000009), uint64(1800000042)

	SetPairRelay("127.0.0.1", 40500, 30)
	defer SetPairRelay("", 0, 0)
	besoinPourTest(t, pidHote, pidVisiteur)
	resetVues()

	s := testSettings()
	ep := NewEndpoint(s)
	hote := NewConnection(ep, "70.40.81.34:1000", func([]byte) {})
	hote.PID = pidHote
	ep.registerConnection(hote)
	visiteur := NewConnection(ep, "201.97.27.16:2000", func([]byte) {})
	visiteur.PID = pidVisiteur
	ep.registerConnection(visiteur)

	// 1. La forme que le pont NAT rend : candidat LAN portant le CID de l hote, puis public.
	urls := stationsHote()
	lanAvant := urls[0].String()

	out := relayedFor(visiteur, hote, urls)
	if len(out) != 2 {
		t.Fatalf("%d stations, attendu 2", len(out))
	}

	// 2. Le candidat LAN doit sortir INTACT : c est lui qui porte le CID dont la Pia du
	//    visiteur a besoin pour monter la session. Le perdre donnait 2618-0502.
	if out[0].String() != lanAvant {
		t.Fatalf("station LAN modifiee:\n avant %s\n apres %s", lanAvant, out[0].String())
	}

	// 3. Aller-retour sur LE CABLE, comme sessionURLsResponse : l echec du 2026-08-30 etait
	//    une erreur de FORME, donc c est sur la forme serialisee qu il faut conclure.
	o := NewStreamOut(s)
	WriteList(o, out, func(w *StreamOut, u *StationURL) { w.StationURL(u) })
	in := NewStreamIn(o.Bytes(), s)
	relues := ReadList(in, func(r *StreamIn) *StationURL { return ParseStationURL(r.String()) })
	if in.Err() != nil {
		t.Fatalf("relecture du cable: %v", in.Err())
	}
	if len(relues) != 2 {
		t.Fatalf("%d stations relues, attendu 2", len(relues))
	}
	if relues[0].GetInt("CID") != 4242 {
		t.Fatalf("CID de l hote perdu sur le cable: %s", relues[0].String())
	}
	if relues[0].Get("address") == relues[1].Get("address") {
		t.Fatal("les deux candidats pointent au meme endroit — reponse degeneree")
	}

	port := relues[1].GetInt("port")
	if relues[1].Get("address") != "127.0.0.1" || port == 0 {
		t.Fatalf("le candidat public ne pointe pas vers le relais: %s", relues[1].String())
	}

	// 4. L autre moitie du serveur doit resoudre au MEME port, sinon chaque console parle
	//    dans une socket que l autre ne lit pas.
	_, portProbe, ok := PairRelayFor(hote.PID, visiteur.PID, PublicIPOf(hote), PublicIPOf(visiteur))
	if !ok || portProbe != port {
		t.Fatalf("cote hote: port %d (ok=%v), cote visiteur: %d", portProbe, ok, port)
	}

	// 5. Le transport lui-meme, avec deux adresses distinctes.
	relais := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	a, ipA := consoleUDP(t, "127.0.0.2")
	b, ipB := consoleUDP(t, "127.1.0.3")

	// Les adresses attendues sont celles des connexions NEX ; on les recale sur les
	// sockets de test.
	pairSocksMu.Lock()
	ps := pairSocks[port]
	pairSocksMu.Unlock()
	if ps == nil {
		t.Fatal("socket de la paire absente")
	}
	ps.mu.Lock()
	ps.attendus = [2]string{ipA, ipB}
	ps.mu.Unlock()

	a.WriteToUDP([]byte("de-a"), relais)
	b.WriteToUDP([]byte("de-b"), relais)
	if got := lireUDP(t, a, "de-b vers a"); got != "de-b" {
		t.Fatalf("a a recu %q", got)
	}
	a.WriteToUDP([]byte("encore-a"), relais)
	if got := lireUDP(t, b, "encore-a vers b"); got != "encore-a" {
		t.Fatalf("b a recu %q", got)
	}

	// 6. Le NAT de A remappe son port source en pleine session.
	a2, _ := consoleUDP(t, "127.0.0.2")
	a2.WriteToUDP([]byte("remappe"), relais)
	if got := lireUDP(t, b, "remappe vers b"); got != "remappe" {
		t.Fatalf("apres remappage, b a recu %q", got)
	}

	// 7. Un tiers ne doit pas pouvoir s inserer.
	intrus, _ := consoleUDP(t, "127.2.0.4")
	intrus.WriteToUDP([]byte("INTRUS"), relais)
	b.WriteToUDP([]byte("apres-intrus"), relais)
	if got := lireUDP(t, a2, "apres-intrus vers a"); got != "apres-intrus" {
		t.Fatalf("l intrus a traverse: a recu %q", got)
	}

	// 8. Un verdict FAILED d une paire relayee ne doit PAS nourrir l eligibilite.
	besoinMu.Lock()
	delete(besoins, visiteur.PID)
	besoinMu.Unlock()
	verdictPercage(t, visiteur, hote.ID, false)
	verdictPercage(t, visiteur, hote.ID, false)
	if BesoinDeRelais(visiteur.PID) {
		t.Fatal("le verdict d une paire relayee a nourri l eligibilite")
	}

	// 9. Les compteurs doivent raconter tout cela.
	st := PairRelayStats()
	var vue *PairStat
	for i := range st.Vivantes {
		if st.Vivantes[i].Port == port {
			vue = &st.Vivantes[i]
		}
	}
	if vue == nil {
		t.Fatal("la paire n apparait pas dans les statistiques")
	}
	if vue.Relayes == 0 {
		t.Fatal("relayes=0 : la paire n a rien porte")
	}
	if vue.Remaps != 1 {
		t.Fatalf("remaps=%d, attendu 1", vue.Remaps)
	}
	if vue.Rejets != 1 {
		t.Fatalf("rejets=%d, attendu 1 (l intrus)", vue.Rejets)
	}
	if vue.Places[0] == "" || vue.Places[1] == "" {
		t.Fatalf("les deux places doivent etre occupees: %v", vue.Places)
	}
}

// TestBoutEnBoutNonVolontaireIntouche : la garantie qui compte le plus. Un joueur hors liste
// ne doit pas voir une seule de ses stations modifiee, ni faire ouvrir un port.
func TestBoutEnBoutNonVolontaireIntouche(t *testing.T) {
	SetPairRelay("127.0.0.1", 40600, 30)
	defer SetPairRelay("", 0, 0)
	// Personne n est autorise.
	SetRelayVolontaires(nil, nil, false)
	resetBesoins()

	s := testSettings()
	ep := NewEndpoint(s)
	hote := NewConnection(ep, "70.40.81.34:1000", func([]byte) {})
	hote.PID = 1800000077
	ep.registerConnection(hote)
	visiteur := NewConnection(ep, "201.97.27.16:2000", func([]byte) {})
	visiteur.PID = 1800000078
	ep.registerConnection(visiteur)

	// Les deux echouent en direct : ils EN ONT BESOIN, mais ne sont pas autorises.
	for i := 0; i < 4; i++ {
		NotePercage(hote.PID, false)
		NotePercage(visiteur.PID, false)
	}

	avant := PairRelayStats().Paires
	urls := stationsHote()
	out := relayedFor(visiteur, hote, urls)

	if len(out) != 2 || out[0] != urls[0] || out[1] != urls[1] {
		t.Fatal("les stations d un non-volontaire ont ete modifiees")
	}
	if apres := PairRelayStats().Paires; apres != avant {
		t.Fatalf("un port a ete ouvert pour un non-volontaire (%d -> %d)", avant, apres)
	}
}

// TestRelaisSousCharge : cinquante paires simultanees, comme un pic de production. Aucune ne
// doit partager un port avec une autre — c est le defaut qui expulsait des joueurs en pleine
// partie, et sa probabilite CROIT avec le nombre de joueurs, donc il ne se voit qu ici.
func TestRelaisSousCharge(t *testing.T) {
	const n = 50
	SetPairRelay("127.0.0.1", 41000, 400)
	defer SetPairRelay("", 0, 0)

	pids := make([]uint64, 0, n*2)
	for i := 0; i < n; i++ {
		pids = append(pids, uint64(2000000+i*2), uint64(2000001+i*2))
	}
	besoinPourTest(t, pids...)

	type attribution struct {
		port  int
		paire [2]uint64
	}
	res := make(chan attribution, n)
	for i := 0; i < n; i++ {
		a, b := pids[i*2], pids[i*2+1]
		go func(a, b uint64) {
			if _, port, ok := PairRelayFor(a, b, "198.51.100.1", "203.0.113.1"); ok {
				res <- attribution{port, [2]uint64{a, b}}

				return
			}
			res <- attribution{0, [2]uint64{a, b}}
		}(a, b)
	}

	parPort := map[int][2]uint64{}
	servies := 0
	for i := 0; i < n; i++ {
		r := <-res
		if r.port == 0 {
			continue
		}
		servies++
		if autre, deja := parPort[r.port]; deja {
			t.Fatalf("le port %d sert deux paires: %v et %v", r.port, autre, r.paire)
		}
		parPort[r.port] = r.paire
	}
	if servies < n {
		t.Fatalf("%d paires servies sur %d — le sondage de collision abandonne trop vite", servies, n)
	}

	// Et le faucheur doit pouvoir tout rendre.
	closeAllPairSockets()
	if st := PairRelayStats(); st.Paires != 0 {
		t.Fatalf("%d paires encore ouvertes apres desarmement", st.Paires)
	}
}
