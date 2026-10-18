package nex

// Plafonds de natpret.go (revue securite du tour 2, point bloquant : epuisement memoire par
// RequestProbeInitiationExt), cas de l'hote entre dans un autre salon, paniques recuperees.
//
// Ces tests n'emploient que des symboles deja presents dans le coeur 1e2d015 : poses sur
// ce commit, ils echouent (ou font tomber le binaire de test, pour les paniques), ce qui
// prouve qu'ils mesurent bien le defaut corrige. Les plafonds y sont donc ecrits en clair.

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	tCiblesMax     = 32 // natCiblesMax
	tRetenusMax    = 16 // natRetenusMax
	tTentativesMax = 32 // natTentativesMax
	tMuetsMax      = 16 // natMuetsMax
	tAttentesMax   = 16 // natAttentesURLMax
)

// probeReqN : RequestProbeInitiationExt qui nomme n fois la cible.
func probeReqN(s *Settings, callID uint32, cible *Connection, n int, station string) *RMCMessage {
	u := ParseStationURL("prudp:/address=203.0.113.50")
	u.SetInt("RVCID", int(cible.ID))
	cibleStr := u.String()
	body := NewStreamOut(s)
	WriteList(body, make([]int, n), func(o *StreamOut, _ int) { o.String(cibleStr) })
	body.String(station)

	return NewRMCRequest(s, ProtocolNATTraversal, MethodRequestProbeInitiationExt, callID, body.Bytes())
}

// initiateProbes compte les InitiateProbe (requetes 0x3.2) recus par une connexion.
func initiateProbes(t *testing.T, s *Settings, c *capte) int {
	t.Helper()
	n := 0
	for _, m := range c.rmc(t, s) {
		if m.Mode == RMCRequest && m.Protocol == ProtocolNATTraversal && m.Method == MethodInitiateProbe {
			n++
		}
	}

	return n
}

func retenusDe(c *Connection) int {
	c.natMu.Lock()
	defer c.natMu.Unlock()

	return len(c.nat.retenus)
}

// Cible prete : avant, chaque entree de la liste partait (1000 entrees = 1000
// InitiateProbe). Maintenant les 32 premieres seulement, chacune exactement comme avant.
func TestProbeListeCiblesPlafonnee(t *testing.T) {
	capterJournalNAT(t)
	s := testSettings()
	ep := NewEndpoint(s)
	appelant, _ := connCapte(ep, 1800990001, "198.51.100.13:51000")
	cible, ccible := connCapte(ep, 1800990003, "203.0.113.20:23781") // jamais remise a zero : prete

	station := fmt.Sprintf("prudp:/address=198.51.100.13;port=63315;RVCID=%d;probeinit=0", appelant.ID)
	if resp := NATTraversalHandler()(appelant, probeReqN(s, 300, cible, 1000, station)); resp == nil || resp.IsError {
		t.Fatal("RequestProbeInitiationExt doit etre acquitte")
	}
	if n := initiateProbes(t, s, ccible); n != tCiblesMax {
		t.Fatalf("1000 entrees : %d InitiateProbe envoyes, attendu %d (plafond)", n, tCiblesMax)
	}
	// Chaque InitiateProbe parti est celui d'avant, a l'octet.
	corps := NewStreamOut(s)
	corps.StationURL(ParseStationURL(station))
	ancien := NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+300, corps.Bytes()).Encode()
	for i, m := range ccible.rmc(t, s) {
		if !bytes.Equal(m.Encode(), ancien) {
			t.Fatalf("InitiateProbe %d different de l'envoi d'avant", i)
		}
	}
}

// Les listes de prod (1 a 3 entrees, parfois la meme cible deux ou trois fois) sont
// servies entree par entree, doublons compris, exactement comme avant.
func TestProbeListesDeProdInchangees(t *testing.T) {
	capterJournalNAT(t)
	s := testSettings()
	ep := NewEndpoint(s)
	appelant, _ := connCapte(ep, 1800990002, "198.51.100.13:51000")
	cible, ccible := connCapte(ep, 1800990003, "203.0.113.20:23781")
	station := fmt.Sprintf("prudp:/address=198.51.100.13;port=63315;RVCID=%d;probeinit=1", appelant.ID)
	for i, n := range []int{1, 2, 3} {
		avant := initiateProbes(t, s, ccible)
		NATTraversalHandler()(appelant, probeReqN(s, uint32(400+i), cible, n, station))
		if got := initiateProbes(t, s, ccible) - avant; got != n {
			t.Errorf("liste de %d entrees : %d InitiateProbe, attendu %d (comme avant)", n, got, n)
		}
	}
}

// LE POINT BLOQUANT. Cible pas prete et attendue (vient d'entrer dans un salon), une seule
// requete de 3,8 Mo qui la nomme 100 000 fois. Avant : 100 000 InitiateProbe retenus (et
// autant de minuteries), +52,6 Mio de tas, pic au-dela de la memoire du conteneur.
// Maintenant : 32 entrees traitees, 16 retenues au plus, les autres envoyees tout de suite
// comme avant ; quelques Kio de tas.
func TestProbeAmplificationMemoireBornee(t *testing.T) {
	capterJournalNAT(t)
	s, _, hote, visiteur, chote, _, _ := salonDeProd(t)
	if hote.natEstPret() {
		t.Fatal("l'hote doit etre pas pret apres autoMatchmake")
	}
	const N = 100000
	req := probeReqN(s, 100, hote, N, fmt.Sprintf("prudp:/address=198.51.100.13;port=63315;RVCID=%d;probeinit=1", visiteur.ID))
	base := initiateProbes(t, s, chote)

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	if resp := NATTraversalHandler()(visiteur, req); resp == nil || resp.IsError {
		t.Fatal("RequestProbeInitiationExt doit etre acquitte")
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	retenu := int64(m1.HeapInuse) - int64(m0.HeapInuse)
	enFile := retenusDe(hote)
	immediats := initiateProbes(t, s, chote) - base
	t.Logf("requete de %d octets (%d cibles) : %d retenus, %d envoyes tout de suite, tas retenu %+d Kio",
		len(req.Body), N, enFile, immediats, retenu>>10)

	if enFile > tRetenusMax {
		t.Fatalf("%d InitiateProbe retenus pour une seule cible, plafond %d", enFile, tRetenusMax)
	}
	if enFile+immediats != tCiblesMax {
		t.Fatalf("%d retenus + %d immediats, attendu %d entrees traitees", enFile, immediats, tCiblesMax)
	}
	if retenu > 4<<20 {
		t.Fatalf("tas retenu %d Kio apres une requete, attendu quelques Kio (avant : +52,6 Mio)", retenu>>10)
	}

	// Le ReplaceURL de la cible libere les retenus, une fois chacun.
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	if n := initiateProbes(t, s, chote) - base; n != tCiblesMax {
		t.Fatalf("apres le ReplaceURL : %d InitiateProbe au total, attendu %d", n, tCiblesMax)
	}
	if r := retenusDe(hote); r != 0 {
		t.Fatalf("%d encore retenus apres le ReplaceURL", r)
	}
}

// Des requetes repetees (chacune sous le plafond de liste) ne font pas grossir la file
// d'une cible au-dela de 16 : au-dela, envoi immediat, exactement le chemin d'avant. Une
// seule ligne de journal pour l'episode de file pleine.
func TestProbeRetenusPlafonnesParCible(t *testing.T) {
	j := capterJournalNAT(t)
	s, _, hote, visiteur, chote, _, _ := salonDeProd(t)
	station := fmt.Sprintf("prudp:/address=198.51.100.13;port=63315;RVCID=%d;probeinit=0", visiteur.ID)
	base := initiateProbes(t, s, chote)
	for i := 0; i < 10; i++ {
		NATTraversalHandler()(visiteur, probeReqN(s, uint32(500+i), hote, tCiblesMax, station))
	}
	enFile := retenusDe(hote)
	immediats := initiateProbes(t, s, chote) - base
	if enFile != tRetenusMax || immediats != 10*tCiblesMax-tRetenusMax {
		t.Fatalf("10 x %d entrees : %d retenus et %d immediats, attendu %d et %d", tCiblesMax, enFile, immediats, tRetenusMax, 10*tCiblesMax-tRetenusMax)
	}
	if n := strings.Count(j.texte(), "deja retenus (plafond)"); n != 1 {
		t.Errorf("%d ligne(s) de file pleine, attendu 1 par episode", n)
	}
}

// Une connexion qui sonde (probeinit=1) beaucoup de cibles distinctes : au plus 32 cibles
// suivies et 16 minuteries « hote muet » a la fois. Avant : une tentative et une minuterie
// de 3 s par entree, sans borne.
func TestTentativesEtMinuteriesPlafonnees(t *testing.T) {
	j := capterJournalNAT(t)
	regler(t, hoteMuetDelai, 80*time.Millisecond)
	s := testSettings()
	ep := NewEndpoint(s)
	appelant, _ := connCapte(ep, 1800990004, "198.51.100.13:51000")
	station := fmt.Sprintf("prudp:/address=198.51.100.13;port=63315;RVCID=%d;probeinit=1", appelant.ID)
	// Seuls les constats de CET appelant : des minuteries d'autres tests ecrivent aussi.
	const muetDe = "de pid=1800990004 (cible"
	const n = 100
	for i := 0; i < n; i++ {
		cible, _ := connCapte(ep, uint64(1800991000+i), "198.51.100.7:40000")
		NATTraversalHandler()(appelant, probeReqN(s, uint32(600+i), cible, 1, station))
	}
	appelant.natMu.Lock()
	suivies := len(appelant.nat.tentatives)
	appelant.natMu.Unlock()
	if suivies > tTentativesMax {
		t.Errorf("%d cibles suivies, plafond %d", suivies, tTentativesMax)
	}
	time.Sleep(300 * time.Millisecond)
	if muets := strings.Count(j.texte(), muetDe); muets > tMuetsMax {
		t.Errorf("%d constats « hote muet » (une minuterie chacun), plafond %d", muets, tMuetsMax)
	}

	// Les minuteries rendues, une nouvelle vague en rearme.
	appelant.natRAZ("autoMatchmake")
	avant := strings.Count(j.texte(), muetDe)
	for i := 0; i < 4; i++ {
		cible, _ := connCapte(ep, uint64(1800992000+i), "198.51.100.8:40000")
		NATTraversalHandler()(appelant, probeReqN(s, uint32(800+i), cible, 1, station))
	}
	time.Sleep(300 * time.Millisecond)
	if got := strings.Count(j.texte(), muetDe) - avant; got != 4 {
		t.Errorf("apres retour des minuteries : %d constats, attendu 4", got)
	}
}

// Un meme visiteur qui boucle GetSessionURLs vers un hote pas pret et attendu : 16
// attentes au plus (une goroutine chacune), les suivantes recoivent tout de suite la
// reponse d'avant, a l'octet. Des visiteurs differents ne se genent pas.
func TestGetSessionURLsAttentesPlafonneesParVisiteur(t *testing.T) {
	capterJournalNAT(t)
	regler(t, natAttenteParticipation, 300*time.Millisecond)
	s, mm, hote, visiteur, _, _, gid := salonDeProd(t)
	mmh := mm.MatchMakingHandler()
	attendu := reponseDAvant(s, mm, hote, visiteur, gid)

	base := runtime.NumGoroutine()
	differees, immediates := 0, 0
	for i := 0; i < 200; i++ {
		resp := mmh(visiteur, sessionURLsReq(s, gid))
		if resp == nil {
			differees++

			continue
		}
		immediates++
		if !bytes.Equal(resp.Body, attendu) {
			t.Fatalf("reponse immediate %d differente de celle d'avant", i)
		}
	}
	pic := runtime.NumGoroutine()
	if differees != tAttentesMax || immediates != 200-tAttentesMax {
		t.Fatalf("200 GetSessionURLs d'un visiteur : %d differees, %d immediates ; attendu %d et %d", differees, immediates, tAttentesMax, 200-tAttentesMax)
	}
	if pic-base > tAttentesMax+5 {
		t.Errorf("%d goroutines de plus pour un visiteur, plafond %d", pic-base, tAttentesMax)
	}

	// Un autre visiteur garde son attente.
	autre, _ := connCapte(hote.Endpoint, 1800990005, "203.0.113.9:40000")
	if resp := mmh(autre, sessionURLsReq(s, gid)); resp != nil {
		t.Error("un autre visiteur doit encore attendre le ReplaceURL de l'hote")
	}

	// Les attentes finies rendent leur place : l'hote refait une participation (son
	// ReplaceURL redevient attendu) et le meme visiteur attend de nouveau.
	time.Sleep(700 * time.Millisecond)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	hote.natRAZ("autoMatchmake")
	if resp := mmh(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Error("apres la fenetre, le visiteur doit pouvoir attendre de nouveau (places rendues)")
	}
}

// Cas theorique de la revue : l'hote sort de son salon et entre dans un AUTRE salon dans
// la seconde. Avant : le visiteur de l'ancien salon attendait jusqu'a 2,5 s les stations
// de la nouvelle session de l'hote. Maintenant : la reponse d'avant, tout de suite.
func TestGetSessionURLsHoteEntreDansUnAutreSalon(t *testing.T) {
	j := capterJournalNAT(t)
	s, mm, hote, visiteur, _, _, gid := salonDeProd(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	endParticipation(s, mm, hote, gid, 51)

	autre := &AutoMatchmakeParam{Session: MatchmakeSession{
		Gathering:         Gathering{MaxParticipants: 4, MinParticipants: 2},
		GameMode:          2,
		OpenParticipation: true,
		Attribs:           []uint32{9, 9, 0, 0, 0, 0},
	}}
	b := NewStreamOut(s)
	b.Add(autre)
	var sess MatchmakeSession
	NewStreamIn(mm.ExtensionHandler()(hote, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 60, b.Bytes())).Body, s).Extract(&sess)
	if sess.ID == gid {
		t.Fatal("l'hote doit entrer dans un autre salon")
	}
	if hote.natEstPret() {
		t.Fatal("l'hote doit etre pas pret apres sa nouvelle participation")
	}
	servieToutDeSuite(t, s, mm, hote, visiteur, gid)
	if !strings.Contains(j.texte(), "hote entre depuis dans un autre salon") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}

	// Le visiteur du NOUVEAU salon, lui, attend toujours son ReplaceURL.
	v2, _ := connCapte(hote.Endpoint, 1800990006, "203.0.113.10:40000")
	NewStreamIn(mm.ExtensionHandler()(v2, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 61, b.Bytes())).Body, s).Extract(&sess)
	if resp := mm.MatchMakingHandler()(v2, sessionURLsReq(s, sess.ID)); resp != nil {
		t.Error("visiteur du nouveau salon : l'attente du ReplaceURL doit rester")
	}
}

// Une panique dans une goroutine ou une minuterie du coeur (hors de la boucle gws, seule
// protegee) tuait le processus, donc le jeu de tous. Ici la sortie du journal panique :
// chaque minuterie et goroutine de natpret doit la recuperer. Pose sur 1e2d015, ce test
// fait tomber le binaire de test.
func TestPaniquesDesMinuteriesNATRecuperees(t *testing.T) {
	var paniques atomic.Int32
	f := func(format string, args ...any) {
		if m := fmt.Sprintf(format, args...); strings.Contains(m, "QUAND MEME") || strings.Contains(m, "hote muet") ||
			strings.Contains(m, "toujours pas pret") {
			paniques.Add(1)
			panic("revue : panique volontaire dans le journal")
		}
	}
	natDiagSortie.Store(&f)
	t.Cleanup(func() { natDiagSortie.Store(nil) })
	regler(t, probeRetenuMax, 50*time.Millisecond)
	regler(t, hoteMuetDelai, 50*time.Millisecond)

	s, mm, hote, visiteur, _, _, gid := salonDeProd(t)
	// Minuterie de retenue (natReverifierRetenu) et minuterie « hote muet ».
	NATTraversalHandler()(visiteur, probeReqN(s, 900, hote, 1, fmt.Sprintf("prudp:/address=198.51.100.13;port=63315;RVCID=%d;probeinit=1", visiteur.ID)))
	// Goroutine de GetSessionURLs : son echeance est hostReplaceURLWait (2,5 s).
	if resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Fatal("hote pas pret et attendu : la reponse doit etre differee")
	}
	time.Sleep(hostReplaceURLWait + 500*time.Millisecond)
	if n := paniques.Load(); n < 3 {
		t.Fatalf("%d paniques declenchees, attendu 3 (retenue, hote muet, GetSessionURLs)", n)
	}
	// La place de l'attente est rendue malgre la panique : jusqu'au plafond, le meme
	// visiteur attend encore.
	natDiagSortie.Store(nil)
	capterJournalNAT(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	hote.natRAZ("autoMatchmake")
	for i := 0; i < tAttentesMax; i++ {
		if resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid)); resp != nil {
			t.Fatalf("attente %d refusee : la place prise par la goroutine qui a panique n'a pas ete rendue", i+1)
		}
	}
	t.Log("3 paniques dans des minuteries et une goroutine du coeur : processus vivant")
}

// Une liste qui nomme trois fois la meme cible (cas de prod du 16/10 03:55:32) : trois
// InitiateProbe partent comme avant, mais c'est UNE tentative (une minuterie « hote
// muet », aucune « relance vers la meme cible »). Une vraie relance (autre appel) reste
// journalisee.
func TestDoublonsDUneListeUneSeuleTentative(t *testing.T) {
	j := capterJournalNAT(t)
	regler(t, hoteMuetDelai, 60*time.Millisecond)
	s := testSettings()
	ep := NewEndpoint(s)
	appelant, _ := connCapte(ep, 1800990007, "198.51.100.14:51000")
	cible, ccible := connCapte(ep, 1800990008, "203.0.113.21:23781")
	station := fmt.Sprintf("prudp:/address=198.51.100.14;port=63315;RVCID=%d;probeinit=1", appelant.ID)

	NATTraversalHandler()(appelant, probeReqN(s, 950, cible, 3, station))
	if n := initiateProbes(t, s, ccible); n != 3 {
		t.Fatalf("%d InitiateProbe, attendu 3 (comme avant)", n)
	}
	if strings.Contains(j.texte(), "relance vers la meme cible") {
		t.Errorf("doublons d'une meme liste : aucune relance attendue, recu :\n%s", j.texte())
	}
	time.Sleep(250 * time.Millisecond)
	if n := strings.Count(j.texte(), "de pid=1800990007 (cible"); n != 1 {
		t.Errorf("%d constat(s) « hote muet », attendu 1 (une seule tentative)", n)
	}

	NATTraversalHandler()(appelant, probeReqN(s, 951, cible, 1, station))
	if !strings.Contains(j.texte(), "abandon silencieux : pid=1800990007 quitte sa tentative vers pid=1800990008 sans verdict") {
		t.Errorf("une vraie relance (autre appel) reste journalisee, recu :\n%s", j.texte())
	}
	time.Sleep(150 * time.Millisecond)
}
