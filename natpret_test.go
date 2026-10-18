package nex

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Hote du 16/10 01:37 : ports et CID releves en prod (TCP du Register 23781, socket P2P de
// sa session 49549, vus par nncs et dans son ReplaceURL) ; son PID et ses adresses (LAN et
// publique) sont remplaces par des valeurs fictives (adresses de documentation, RFC 5737).
const (
	pretHoteIP   = "198.51.100.31"
	pretHoteLAN  = "192.168.1.54"
	pretHoteUDP  = 49549
	pretHoteCID  = 3650518749
	pretHoteAddr = pretHoteIP + ":23781"
)

// capte garde les trames qu'une connexion envoie (sa fonction send).
type capte struct {
	mu     sync.Mutex
	trames [][]byte
}

func (c *capte) send(b []byte) {
	c.mu.Lock()
	c.trames = append(c.trames, append([]byte(nil), b...))
	c.mu.Unlock()
}

// rmc decode les messages RMC envoyes, dans l'ordre d'envoi.
func (c *capte) rmc(t *testing.T, s *Settings) []*RMCMessage {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*RMCMessage
	for _, f := range c.trames {
		p := decodeOne(t, f)
		if p.Type != PacketDATA {
			continue
		}
		m, err := ParseRMC(s, p.Payload)
		if err != nil {
			t.Fatalf("RMC illisible : %v", err)
		}
		out = append(out, m)
	}

	return out
}

// attendre relit la capture jusqu'a n messages ou expiration.
func (c *capte) attendre(t *testing.T, s *Settings, n int, max time.Duration) []*RMCMessage {
	t.Helper()
	fin := time.Now().Add(max)
	for {
		m := c.rmc(t, s)
		if len(m) >= n || time.Now().After(fin) {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// journalNAT detourne natDiagf le temps du test et rend ce qui y est ecrit.
type journalNAT struct {
	mu sync.Mutex
	b  strings.Builder
}

func (j *journalNAT) texte() string {
	j.mu.Lock()
	defer j.mu.Unlock()

	return j.b.String()
}

func capterJournalNAT(t *testing.T) *journalNAT {
	t.Helper()
	j := &journalNAT{}
	f := func(format string, args ...any) {
		j.mu.Lock()
		fmt.Fprintf(&j.b, format, args...)
		j.mu.Unlock()
	}
	natDiagSortie.Store(&f)
	t.Cleanup(func() { natDiagSortie.Store(nil) })

	return j
}

func connCapte(ep *Endpoint, pid uint64, addr string) (*Connection, *capte) {
	c := &capte{}
	conn := NewConnection(ep, addr, c.send)
	conn.PID = pid
	ep.registerConnection(conn)

	return conn, c
}

// registerReq : la console declare seulement sa station LAN (port=1, natf=0, sans CID),
// comme dans le Register de prod du 16/10 01:36:13.986.
func registerReq(s *Settings) *RMCMessage {
	lan := ParseStationURL("prudp:/address=" + pretHoteLAN + ";port=1;Pl=3;Tpt=2;natf=0;natm=0;pmp=0;sid=30;upnp=0")
	body := NewStreamOut(s)
	WriteList(body, []*StationURL{lan}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })

	return NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 1, body.Bytes())
}

// replaceURLReq : le ReplaceURL d'une console apres son NAT check (cible ignoree, puis
// la nouvelle station LAN).
func replaceURLReq(s *Settings, callID uint32, st *StationURL) *RMCMessage {
	body := NewStreamOut(s)
	body.StationURL(st)
	body.StationURL(st)

	return NewRMCRequest(s, ProtocolSecureConnection, MethodReplaceURL, callID, body.Bytes())
}

func stationReplaceURLHote(rvcid uint32, cid int) *StationURL {
	return ParseStationURL(fmt.Sprintf("prudp:/address=%s;port=%d;CID=%d;Pl=3;RVCID=%d;Tpt=2;natf=17;natm=1;pmp=0;sid=30;upnp=0",
		pretHoteLAN, pretHoteUDP, cid, rvcid))
}

func autoMatchmakeReq(s *Settings) *RMCMessage {
	p := &AutoMatchmakeParam{Session: MatchmakeSession{
		Gathering:         Gathering{MaxParticipants: 4, MinParticipants: 2},
		GameMode:          1,
		OpenParticipation: true,
		Attribs:           []uint32{2, 11, 0, 0, 0, 0},
	}}
	body := NewStreamOut(s)
	body.Add(p)

	return NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 2, body.Bytes())
}

func sessionURLsReq(s *Settings, gid uint32) *RMCMessage {
	b := NewStreamOut(s)
	b.U32(gid)

	return NewRMCRequest(s, ProtocolMatchMaking, MethodGetSessionURLs, 7, b.Bytes())
}

func probeReq(s *Settings, callID uint32, cible *Connection, station string) *RMCMessage {
	cibleURL := NewStationURL("prudp")
	cibleURL.Set("address", "203.0.113.50")
	cibleURL.SetInt("RVCID", int(cible.ID))
	body := NewStreamOut(s)
	WriteList(body, []string{cibleURL.String()}, func(o *StreamOut, u string) { o.String(u) })
	body.String(station)

	return NewRMCRequest(s, ProtocolNATTraversal, MethodRequestProbeInitiationExt, callID, body.Bytes())
}

func urlsDe(s *Settings, m *RMCMessage) []*StationURL {
	return ReadList(NewStreamIn(m.Body, s), func(i *StreamIn) *StationURL { return i.StationURLValue() })
}

// salonDeProd monte la scene du 16/10 01:37:54 : l'hote se connecte (Register), cree son
// salon (autoMatchmake) ; un visiteur entre dans le salon.
func salonDeProd(t *testing.T) (s *Settings, mm *Matchmaking, hote, visiteur *Connection, chote, cvis *capte, gid uint32) {
	t.Helper()
	writeNatFile(t, pretHoteIP+" 49549\n")
	s = testSettings()
	ep := NewEndpoint(s)
	mm = NewMatchmaking()
	sc := SecureConnectionHandler()
	ext := mm.ExtensionHandler()

	hote, chote = connCapte(ep, 1800900004, pretHoteAddr)
	visiteur, cvis = connCapte(ep, 1800900001, "198.51.100.34:51000")

	sc(hote, registerReq(s))
	var sess MatchmakeSession
	NewStreamIn(ext(hote, autoMatchmakeReq(s)).Body, s).Extract(&sess)
	gid = sess.ID
	NewStreamIn(ext(visiteur, autoMatchmakeReq(s)).Body, s).Extract(&sess)
	if sess.ID != gid {
		t.Fatalf("le visiteur doit entrer dans le salon de l'hote (gid %d, recu %d)", gid, sess.ID)
	}

	return
}

func TestRemisesAZeroEtReplaceURL(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	sc := SecureConnectionHandler()
	c, _ := connCapte(ep, 1800000006, "198.51.100.32:50000")

	if !c.natEstPret() {
		t.Fatal("une connexion jamais remise a zero doit etre prete (comportement d'avant)")
	}
	sc(c, registerReq(s))
	if c.natEstPret() {
		t.Fatal("apres le Register, la connexion n'est pas prete (port=1, sans CID)")
	}
	if resp := sc(c, replaceURLReq(s, 3, stationReplaceURLHote(c.ID, 1))); resp == nil || resp.IsError {
		t.Fatal("sans InitiateProbe retenu, le ReplaceURL rend sa reponse comme avant")
	}
	if !c.natEstPret() {
		t.Fatal("apres un ReplaceURL, la connexion est prete")
	}

	ext := mm.ExtensionHandler()
	mmx := mm.MatchMakingExtHandler()
	var gidCree uint32
	etapes := []struct {
		nom string
		f   func()
	}{
		{"autoMatchmake", func() { ext(c, autoMatchmakeReq(s)) }},
		{"EndParticipation", func() {
			b := NewStreamOut(s)
			b.U32(1)
			mmx(c, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 4, b.Bytes()))
		}},
		{"createSession", func() {
			b := NewStreamOut(s)
			b.Add(&AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}, Attribs: []uint32{0, 0, 0, 0, 0, 0}}})
			var sess MatchmakeSession
			NewStreamIn(ext(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCreateMatchmakeSessionWithParam, 5, b.Bytes())).Body, s).Extract(&sess)
			gidCree = sess.ID
		}},
		{"joinSession", func() {
			sub := NewStreamOut(s)
			sub.U32(gidCree) // salon cree a l'etape precedente
			b := NewStreamOut(s)
			b.U8(0)
			b.Buffer(sub.Bytes())
			if resp := ext(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodJoinMatchmakeSessionWithParam, 6, b.Bytes())); resp == nil || resp.IsError {
				t.Fatalf("joinSession refuse : %+v", resp)
			}
		}},
	}
	for i, e := range etapes {
		sc(c, replaceURLReq(s, uint32(10+i), stationReplaceURLHote(c.ID, 100+i)))
		if !c.natEstPret() {
			t.Fatalf("%s : la connexion devait etre prete avant", e.nom)
		}
		e.f()
		if c.natEstPret() {
			t.Errorf("%s doit remettre la connexion a zero", e.nom)
		}
		if quoi, _ := c.natRaz(); quoi != e.nom {
			t.Errorf("cause de remise a zero = %q, attendu %q", quoi, e.nom)
		}
	}
}

// Le cas de prod : le visiteur demande les URL de l'hote avant son ReplaceURL. Avant ce
// correctif il recevait le port observe au Register (perime) sans CID ni natf/natm ;
// maintenant il attend, et recoit les stations de la nouvelle session.
func TestHotePasPretLeVisiteurAttendSonReplaceURL(t *testing.T) {
	j := capterJournalNAT(t)
	s, mm, hote, visiteur, _, cvis, gid := salonDeProd(t)
	mmh := mm.MatchMakingHandler()
	sc := SecureConnectionHandler()
	base := len(cvis.rmc(t, s)) // notifications de participation deja recues

	if resp := mmh(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Fatalf("hote pas pret : la reponse doit attendre son ReplaceURL, recu %+v", urlsDe(s, resp))
	}
	time.Sleep(250 * time.Millisecond)
	if n := len(cvis.rmc(t, s)) - base; n != 0 {
		t.Fatalf("le visiteur a recu %d message(s) avant le ReplaceURL de l'hote", n)
	}

	debut := time.Now()
	sc(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	msgs := cvis.attendre(t, s, base+1, time.Second)[base:]
	if len(msgs) != 1 {
		t.Fatalf("le visiteur doit recevoir une reponse juste apres le ReplaceURL, recu %d", len(msgs))
	}
	if d := time.Since(debut); d > 500*time.Millisecond {
		t.Errorf("reponse %v apres le ReplaceURL, attendu au plus un tour de sondage", d)
	}
	urls := urlsDe(s, msgs[0])
	if len(urls) != 2 {
		t.Fatalf("pont attendu [lan, public], recu %v", urls)
	}
	lan, pub := urls[0], urls[1]
	if lan.Get("address") != pretHoteLAN || lan.GetInt("port") != pretHoteUDP || lan.GetInt("CID") != pretHoteCID || lan.GetInt("natf") != 17 {
		t.Errorf("station LAN = %s, attendu celle du ReplaceURL (port %d, CID %d, natf=17)", lan, pretHoteUDP, pretHoteCID)
	}
	if pub.Get("address") != pretHoteIP || pub.GetInt("port") != pretHoteUDP {
		t.Errorf("station publique = %s", pub)
	}
	if !strings.Contains(j.texte(), "hote pid=1800900004 pas pret (autoMatchmake") {
		t.Errorf("journal attendu sur l'attente, recu :\n%s", j.texte())
	}
}

// Un hote qui ne fait jamais son ReplaceURL : passe 2,5 s, le visiteur recoit exactement
// ce que le serveur repondait avant ce correctif.
func TestHotePasPretSansReplaceURLRepondCommeAvant(t *testing.T) {
	capterJournalNAT(t)
	s, mm, hote, visiteur, _, cvis, gid := salonDeProd(t)

	// Ce que l'ancien code repondait tout de suite.
	avant, _ := natBridgeStations(hote.Stations(), mm.PublicStationFirst)
	attendu := sessionURLsResponse(visiteur, sessionURLsReq(s, gid), avant).Body
	base := len(cvis.rmc(t, s))

	if resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Fatal("hote pas pret : la reponse doit attendre")
	}
	msgs := cvis.attendre(t, s, base+1, hostReplaceURLWait+time.Second)[base:]
	if len(msgs) != 1 {
		t.Fatalf("passe le delai, une reponse doit partir quand meme (recu %d)", len(msgs))
	}
	if !bytes.Equal(msgs[0].Body, attendu) {
		t.Errorf("reponse apres delai differente de l'ancienne :\n got %v\nwant %v", urlsDe(s, msgs[0]), avant)
	}
}

// Non-regression : un hote pret est servi tout de suite, octet pour octet comme avant.
func TestHotePretInchange(t *testing.T) {
	j := capterJournalNAT(t)
	s, mm, hote, visiteur, _, _, gid := salonDeProd(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))

	avant, status := natBridgeStations(hote.Stations(), mm.PublicStationFirst)
	if status != bridgeOK {
		t.Fatalf("le pont doit marcher pour cet hote (status %d)", status)
	}
	attendu := sessionURLsResponse(visiteur, sessionURLsReq(s, gid), avant).Body

	resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid))
	if resp == nil {
		t.Fatal("hote pret : la reponse doit partir tout de suite")
	}
	if !bytes.Equal(resp.Body, attendu) {
		t.Errorf("reponse differente de l'ancien code")
	}
	if strings.Contains(j.texte(), "GetSessionURLs") {
		t.Errorf("aucun journal d'attente pour un hote pret, recu :\n%s", j.texte())
	}
}

// L'hote qui demande les URL de son propre salon n'attend pas son propre ReplaceURL.
func TestHoteDemandeSesPropresURL(t *testing.T) {
	capterJournalNAT(t)
	s, mm, hote, _, _, _, gid := salonDeProd(t)
	if resp := mm.MatchMakingHandler()(hote, sessionURLsReq(s, gid)); resp == nil {
		t.Fatal("l'hote doit recevoir ses propres URL sans attendre")
	}
}

// L'InitiateProbe pousse a un hote pas pret attend son ReplaceURL et part juste apres la
// reponse a celui-ci (cas du 16/10 01:37:57.103, hote pret a 01:37:57.817).
func TestInitiateProbeRetenuPuisLibereParLeReplaceURL(t *testing.T) {
	j := capterJournalNAT(t)
	s, _, hote, visiteur, chote, _, _ := salonDeProd(t)
	nat := NATTraversalHandler()
	sc := SecureConnectionHandler()
	avantProbe := len(chote.rmc(t, s))

	station := fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;CID=2385962744;PID=1800900001;RVCID=%d;natf=17;natm=1;probeinit=1;sid=30;type=11", visiteur.ID)
	if resp := nat(visiteur, probeReq(s, 100, hote, station)); resp == nil || resp.IsError {
		t.Fatal("RequestProbeInitiationExt doit etre acquitte tout de suite")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(chote.rmc(t, s)) - avantProbe; n != 0 {
		t.Fatalf("hote pas pret : %d InitiateProbe envoye(s) avant son ReplaceURL", n)
	}

	if resp := sc(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID))); resp != nil {
		t.Fatal("avec un InitiateProbe retenu, le ReplaceURL envoie lui-meme sa reponse puis le probe")
	}
	msgs := chote.rmc(t, s)[avantProbe:]
	if len(msgs) != 2 {
		t.Fatalf("attendu [reponse ReplaceURL, InitiateProbe], recu %d message(s)", len(msgs))
	}
	if msgs[0].Mode != RMCResponse || msgs[0].Method != MethodReplaceURL {
		t.Errorf("1er message : %+v, attendu la reponse au ReplaceURL", msgs[0])
	}
	if msgs[1].Mode != RMCRequest || msgs[1].Method != MethodInitiateProbe || msgs[1].CallID != 0xFFFF0000+100 {
		t.Errorf("2e message : %+v, attendu l'InitiateProbe", msgs[1])
	}
	if got := NewStreamIn(msgs[1].Body, s).String(); got != ParseStationURL(station).String() {
		t.Errorf("station poussee = %q", got)
	}
	if !strings.Contains(j.texte(), "RETENU") || !strings.Contains(j.texte(), "LIBERE par son ReplaceURL") {
		t.Errorf("journaux de retenue attendus, recu :\n%s", j.texte())
	}

	// Un ReplaceURL suivant ne renvoie rien de plus.
	if resp := sc(hote, replaceURLReq(s, 28, stationReplaceURLHote(hote.ID, pretHoteCID))); resp == nil {
		t.Error("plus rien en file : le ReplaceURL rend sa reponse comme avant")
	}
}

// Une cible qui ne fait jamais son ReplaceURL recoit l'InitiateProbe quand meme, une fois,
// au bout du delai.
func TestInitiateProbeEnvoyeQuandMemeApresLeDelai(t *testing.T) {
	j := capterJournalNAT(t)
	avant := probeRetenuMax.get()
	probeRetenuMax.set(80 * time.Millisecond)
	t.Cleanup(func() { probeRetenuMax.set(avant) })

	s, _, hote, visiteur, chote, _, _ := salonDeProd(t)
	base := len(chote.rmc(t, s))
	station := fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=1", visiteur.ID)
	NATTraversalHandler()(visiteur, probeReq(s, 101, hote, station))

	msgs := chote.attendre(t, s, base+1, time.Second)[base:]
	if len(msgs) != 1 || msgs[0].Protocol != ProtocolNATTraversal || msgs[0].Method != MethodInitiateProbe {
		t.Fatalf("attendu un InitiateProbe apres le delai, recu %d", len(msgs))
	}
	if !strings.Contains(j.texte(), "envoye QUAND MEME") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}

	// Le ReplaceURL qui arrive ensuite ne le renvoie pas.
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	time.Sleep(50 * time.Millisecond)
	n := 0
	for _, m := range chote.rmc(t, s)[base:] {
		if m.Protocol == ProtocolNATTraversal && m.Method == MethodInitiateProbe && m.Mode == RMCRequest {
			n++
		}
	}
	if n != 1 {
		t.Errorf("InitiateProbe envoye %d fois, attendu 1", n)
	}
}

// Non-regression : une cible prete recoit l'InitiateProbe tout de suite, identique a
// l'ancien envoi.
func TestInitiateProbeCiblePreteInchange(t *testing.T) {
	capterJournalNAT(t)
	s, _, hote, visiteur, chote, _, _ := salonDeProd(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	base := len(chote.rmc(t, s))

	station := fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=1", visiteur.ID)
	NATTraversalHandler()(visiteur, probeReq(s, 102, hote, station))

	msgs := chote.rmc(t, s)[base:]
	if len(msgs) != 1 {
		t.Fatalf("cible prete : un InitiateProbe immediat attendu, recu %d", len(msgs))
	}
	corps := NewStreamOut(s)
	corps.StationURL(ParseStationURL(station))
	ancien := NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+102, corps.Bytes())
	if !bytes.Equal(msgs[0].Encode(), ancien.Encode()) {
		t.Error("InitiateProbe different de l'ancien envoi")
	}
}

func TestHoteMuetEtAbandonSilencieux(t *testing.T) {
	j := capterJournalNAT(t)
	avant := hoteMuetDelai.get()
	hoteMuetDelai.set(60 * time.Millisecond)
	t.Cleanup(func() { hoteMuetDelai.set(avant) })

	s, mm, hote, visiteur, _, _, _ := salonDeProd(t)
	sc := SecureConnectionHandler()
	nat := NATTraversalHandler()
	sc(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	sc(visiteur, replaceURLReq(s, 30, ParseStationURL(fmt.Sprintf("prudp:/address=10.0.0.82;port=63315;CID=2385962744;RVCID=%d", visiteur.ID))))
	st := fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=1", visiteur.ID)

	// 1. L'hote ne repond pas : « hote muet », puis la sortie du visiteur sans verdict
	//    donne « abandon silencieux ».
	nat(visiteur, probeReq(s, 200, hote, st))
	time.Sleep(150 * time.Millisecond)
	if !strings.Contains(j.texte(), "hote muet : pid=1800900004 n'a pas repondu") {
		t.Errorf("« hote muet » attendu, recu :\n%s", j.texte())
	}
	b := NewStreamOut(s)
	b.U32(1)
	mm.MatchMakingExtHandler()(visiteur, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 31, b.Bytes()))
	if !strings.Contains(j.texte(), "abandon silencieux : pid=1800900001 quitte sa tentative vers pid=1800900004") {
		t.Errorf("« abandon silencieux » attendu, recu :\n%s", j.texte())
	}

	// 2. L'hote repond (0x3.3 vers le visiteur) puis le verdict arrive : aucun des deux journaux.
	sc(visiteur, replaceURLReq(s, 32, ParseStationURL(fmt.Sprintf("prudp:/address=10.0.0.82;port=63312;CID=1;RVCID=%d", visiteur.ID))))
	jAvant := j.texte()
	nat(visiteur, probeReq(s, 201, hote, st))
	nat(hote, probeReq(s, 202, visiteur, fmt.Sprintf("prudp:/address=%s;port=%d;RVCID=%d;probeinit=0", pretHoteIP, pretHoteUDP, hote.ID)))
	time.Sleep(150 * time.Millisecond)
	v := NewStreamOut(s)
	v.U32(hote.ID)
	v.Bool(true)
	v.U32(120)
	nat(visiteur, NewRMCRequest(s, ProtocolNATTraversal, MethodReportNATTraversalResult, 203, v.Bytes()))
	mm.MatchMakingExtHandler()(visiteur, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 204, b.Bytes()))
	if nouveau := strings.TrimPrefix(j.texte(), jAvant); strings.Contains(nouveau, "hote muet") || strings.Contains(nouveau, "abandon silencieux") {
		t.Errorf("hote qui repond + verdict : aucun constat attendu, recu :\n%s", nouveau)
	}

	// 3. Un ReplaceURL en double (meme CID) n'est pas un abandon ; un nouveau CID l'est.
	sc(visiteur, replaceURLReq(s, 40, ParseStationURL(fmt.Sprintf("prudp:/address=10.0.0.82;port=63300;CID=7;RVCID=%d", visiteur.ID))))
	nat(visiteur, probeReq(s, 205, hote, st))
	jAvant = j.texte()
	sc(visiteur, replaceURLReq(s, 41, ParseStationURL(fmt.Sprintf("prudp:/address=10.0.0.82;port=63300;CID=7;RVCID=%d", visiteur.ID))))
	if strings.Contains(strings.TrimPrefix(j.texte(), jAvant), "abandon silencieux") {
		t.Error("un ReplaceURL en double (meme CID) n'abandonne pas la tentative")
	}
	sc(visiteur, replaceURLReq(s, 42, ParseStationURL(fmt.Sprintf("prudp:/address=10.0.0.82;port=63301;CID=8;RVCID=%d", visiteur.ID))))
	if !strings.Contains(strings.TrimPrefix(j.texte(), jAvant), "abandon silencieux : pid=1800900001 quitte sa tentative vers pid=1800900004 sans verdict") ||
		!strings.Contains(j.texte(), "cause : nouveau ReplaceURL") {
		t.Errorf("un nouveau CID abandonne la tentative, recu :\n%s", strings.TrimPrefix(j.texte(), jAvant))
	}
	time.Sleep(100 * time.Millisecond) // laisse partir les minuteries avant de rendre natDiagf
}

// ---------------------------------------------------------------------------------------
// Affinage : on n'attend / ne retient que si le ReplaceURL de l'hote est encore attendu.
// ---------------------------------------------------------------------------------------

// regler raccourcit une duree le temps d'un test.
func regler(t *testing.T, d *dureeReglable, v time.Duration) {
	t.Helper()
	avant := d.get()
	d.set(v)
	t.Cleanup(func() { d.set(avant) })
}

// reponseDAvant : ce que getSessionURLs repondait tout de suite avant le correctif.
func reponseDAvant(s *Settings, mm *Matchmaking, hote, visiteur *Connection, gid uint32) []byte {
	avant, _ := natBridgeStations(hote.Stations(), mm.PublicStationFirst)

	return sessionURLsResponse(visiteur, sessionURLsReq(s, gid), avant).Body
}

func endParticipation(s *Settings, mm *Matchmaking, c *Connection, gid uint32, callID uint32) {
	b := NewStreamOut(s)
	b.U32(gid)
	mm.MatchMakingExtHandler()(c, NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, callID, b.Bytes()))
}

func closeParticipation(s *Settings, mm *Matchmaking, c *Connection, gid uint32) {
	b := NewStreamOut(s)
	b.U32(gid)
	mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCloseParticipation, 50, b.Bytes()))
}

// reponsesURL garde les reponses a GetSessionURLs (pas les notifications de participation).
func reponsesURL(msgs []*RMCMessage) []*RMCMessage {
	var out []*RMCMessage
	for _, m := range msgs {
		if m.Mode == RMCResponse && m.Protocol == ProtocolMatchMaking && m.Method == MethodGetSessionURLs {
			out = append(out, m)
		}
	}

	return out
}

// attendreReponseURL attend une reponse a GetSessionURLs apres les base premiers messages.
func attendreReponseURL(t *testing.T, s *Settings, c *capte, base int, max time.Duration) []*RMCMessage {
	t.Helper()
	fin := time.Now().Add(max)
	for {
		msgs := reponsesURL(c.rmc(t, s)[base:])
		if len(msgs) > 0 || time.Now().After(fin) {
			return msgs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// servieToutDeSuite verifie que getSessionURLs repond sur-le-champ, octet pour octet comme
// avant le correctif.
func servieToutDeSuite(t *testing.T, s *Settings, mm *Matchmaking, hote, visiteur *Connection, gid uint32) {
	t.Helper()
	attendu := reponseDAvant(s, mm, hote, visiteur, gid)
	resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid))
	if resp == nil {
		t.Fatal("ReplaceURL pas attendu : la reponse doit partir tout de suite")
	}
	if !bytes.Equal(resp.Body, attendu) {
		t.Errorf("reponse differente de celle d'avant :\n got %v", urlsDe(s, resp))
	}
}

func TestNatAttenduRegles(t *testing.T) {
	regler(t, natAttenteParticipation, 6*time.Second)
	regler(t, natAttenteRetour, time.Second)
	c := &Connection{}
	t0 := time.Now()
	cas := []struct {
		nom            string
		etat           natEtat
		age            time.Duration
		salonOuvert    bool
		attendu        bool
		raisonContient string
	}{
		{"prete", natEtat{}, 0, true, false, "prete"},
		{"Register", natEtat{pasPret: true, razGenre: natGenreRegister, razQuoi: "Register"}, 0, true, false, "Register"},
		{"participation fraiche", natEtat{pasPret: true, razGenre: natGenreParticipation, razQuoi: "autoMatchmake"}, 3650 * time.Millisecond, true, true, ""},
		{"participation ancienne", natEtat{pasPret: true, razGenre: natGenreParticipation, razQuoi: "autoMatchmake"}, 6 * time.Second, true, false, "plus de 6s"},
		{"participation, salon ferme sans effet", natEtat{pasPret: true, razGenre: natGenreParticipation, razQuoi: "joinSession"}, time.Second, false, true, ""},
		{"sortie fraiche, salon ouvert", natEtat{pasPret: true, razGenre: natGenreSortie, razQuoi: "EndParticipation"}, 390 * time.Millisecond, true, true, ""},
		{"sortie fraiche, salon ferme", natEtat{pasPret: true, razGenre: natGenreSortie, razQuoi: "EndParticipation"}, 390 * time.Millisecond, false, false, "salon ferme"},
		{"sortie sans retour", natEtat{pasPret: true, razGenre: natGenreSortie, razQuoi: "EndParticipation"}, time.Second, true, false, "sans retour"},
		{"console qui sonde deja", natEtat{pasPret: true, razGenre: natGenreParticipation, razQuoi: "joinSession", sonde: true}, 0, true, false, "sonde"},
		{"console muette", natEtat{pasPret: true, razGenre: natGenreParticipation, razQuoi: "autoMatchmake", muette: true}, 0, true, false, "sans ReplaceURL"},
	}
	for _, k := range cas {
		c.nat = k.etat
		c.nat.razQuand = t0.Add(-k.age)
		ok, raison := c.natAttenduLocked(t0, k.salonOuvert)
		if ok != k.attendu || !strings.Contains(raison, k.raisonContient) {
			t.Errorf("%s : natAttendu = %v (%q), attendu %v (%q)", k.nom, ok, raison, k.attendu, k.raisonContient)
		}
	}
}

// muette : posee quand une participation entiere passe sans ReplaceURL, effacee par le
// ReplaceURL ; sonde : posee par RequestProbeInitiationExt, effacee par la remise a zero.
func TestMuetteEtSondePoseesEtEffacees(t *testing.T) {
	capterJournalNAT(t)
	regler(t, natAttenteParticipation, 40*time.Millisecond)
	s := testSettings()
	ep := NewEndpoint(s)
	c, _ := connCapte(ep, 1800900003, "198.51.100.33:45191")
	cible, _ := connCapte(ep, 1800900002, "203.0.113.50:50000")

	c.natRAZ("autoMatchmake")
	c.natRAZ("EndParticipation") // participation coupee avant son budget : pas muette
	c.natRAZ("autoMatchmake")
	if ok, _ := c.natAttendu(true); !ok {
		t.Fatal("une participation coupee avant son budget ne rend pas la console muette")
	}
	time.Sleep(60 * time.Millisecond)
	c.natRAZ("autoMatchmake") // la precedente a dure plus que le budget sans ReplaceURL
	if ok, raison := c.natAttendu(true); ok || !strings.Contains(raison, "sans ReplaceURL") {
		t.Fatalf("console muette : ReplaceURL pas attendu, recu %v (%q)", ok, raison)
	}
	SecureConnectionHandler()(c, replaceURLReq(s, 3, stationReplaceURLHote(c.ID, 5)))
	c.natRAZ("autoMatchmake")
	if ok, _ := c.natAttendu(true); !ok {
		t.Fatal("le ReplaceURL efface « muette »")
	}

	NATTraversalHandler()(c, probeReq(s, 9, cible, fmt.Sprintf("prudp:/address=198.51.100.33;port=45191;RVCID=%d;probeinit=1", c.ID)))
	if ok, raison := c.natAttendu(true); ok || !strings.Contains(raison, "sonde") {
		t.Fatalf("console qui sonde : ReplaceURL pas attendu, recu %v (%q)", ok, raison)
	}
	c.natRAZ("joinSession")
	if ok, _ := c.natAttendu(true); !ok {
		t.Fatal("la remise a zero efface « sonde »")
	}
}

// Register seul : aucun ReplaceURL ne vient avant une participation (0 sur 1619 en prod) :
// le visiteur recoit la reponse d'avant sans attendre.
func TestHoteRegisterSeulServiSansAttente(t *testing.T) {
	j := capterJournalNAT(t)
	s, mm, hote, visiteur, _, _, gid := salonDeProd(t)
	sc := SecureConnectionHandler()
	sc(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	sc(hote, registerReq(s))
	if hote.natEstPret() {
		t.Fatal("le Register remet la connexion a zero")
	}
	servieToutDeSuite(t, s, mm, hote, visiteur, gid)
	if !strings.Contains(j.texte(), "aucun ReplaceURL attendu (Register seul") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
}

// Participation plus vieille que son budget (cas de 1800900003, 104 attentes le 15/10) :
// le visiteur recoit la reponse d'avant sans attendre, l'InitiateProbe part sans retenue.
func TestParticipationTropAnciennePasAttendue(t *testing.T) {
	j := capterJournalNAT(t)
	regler(t, natAttenteParticipation, 50*time.Millisecond)
	s, mm, hote, visiteur, chote, _, gid := salonDeProd(t)
	time.Sleep(70 * time.Millisecond)
	servieToutDeSuite(t, s, mm, hote, visiteur, gid)

	base := len(chote.rmc(t, s))
	station := fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=1", visiteur.ID)
	NATTraversalHandler()(visiteur, probeReq(s, 103, hote, station))
	msgs := chote.rmc(t, s)[base:]
	if len(msgs) != 1 || msgs[0].Method != MethodInitiateProbe {
		t.Fatalf("InitiateProbe immediat attendu, recu %d message(s)", len(msgs))
	}
	corps := NewStreamOut(s)
	corps.StationURL(ParseStationURL(station))
	if !bytes.Equal(msgs[0].Encode(), NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+103, corps.Bytes()).Encode()) {
		t.Error("InitiateProbe different de l'ancien envoi")
	}
	if !strings.Contains(j.texte(), "envoye sans retenue") || strings.Contains(j.texte(), "RETENU") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
}

// L'hote sort d'un salon ferme (fin de match) : il ne peut pas y revenir par autoMatchmake,
// rien a attendre (22 attentes de 1 s sur les journaux de prod, toutes dans ce cas).
func TestSortieDUnSalonFermePasAttendue(t *testing.T) {
	j := capterJournalNAT(t)
	s, mm, hote, visiteur, _, _, gid := salonDeProd(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	closeParticipation(s, mm, hote, gid)
	endParticipation(s, mm, hote, gid, 51)
	if hote.natEstPret() {
		t.Fatal("EndParticipation remet la connexion a zero")
	}
	servieToutDeSuite(t, s, mm, hote, visiteur, gid)
	if !strings.Contains(j.texte(), "salon ferme") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
}

// L'hote sort de son salon encore ouvert et n'y revient pas : l'attente est coupee a la fin
// du budget de retour, et le visiteur recoit la reponse d'avant, pas 2,5 s apres.
func TestSortieSansRetourCoupeLAttente(t *testing.T) {
	j := capterJournalNAT(t)
	regler(t, natAttenteRetour, 100*time.Millisecond)
	s, mm, hote, visiteur, _, cvis, gid := salonDeProd(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	endParticipation(s, mm, hote, gid, 51)
	attendu := reponseDAvant(s, mm, hote, visiteur, gid)
	base := len(cvis.rmc(t, s))

	debut := time.Now()
	if resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Fatal("sortie a l'instant d'un salon ouvert : on attend son retour")
	}
	msgs := attendreReponseURL(t, s, cvis, base, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("reponse attendue a la fin du budget de retour, recu %d", len(msgs))
	}
	if d := time.Since(debut); d > 400*time.Millisecond {
		t.Errorf("reponse %v apres la demande, attendu le budget de retour (100 ms) plus un tour", d)
	}
	if !bytes.Equal(msgs[0].Body, attendu) {
		t.Errorf("reponse differente de celle d'avant : %v", urlsDe(s, msgs[0]))
	}
	if !strings.Contains(j.texte(), "ReplaceURL plus attendu (sortie il y a") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
}

// Cas du 15/10 22:46:36 : l'hote sort de son salon encore ouvert, y revient 0,7 s plus tard
// par autoMatchmake et fait son ReplaceURL 0,77 s apres. Le retour relance le budget :
// l'attente continue au-dela du budget de retour, et le visiteur recoit le nouveau port.
func TestSortiePuisRetourDansLeSalonGardeLeGagnant(t *testing.T) {
	capterJournalNAT(t)
	regler(t, natAttenteRetour, 150*time.Millisecond)
	s, mm, hote, visiteur, _, cvis, gid := salonDeProd(t)
	sc := SecureConnectionHandler()
	sc(hote, replaceURLReq(s, 27, ParseStationURL(fmt.Sprintf("prudp:/address=%s;port=60541;CID=1;Pl=3;RVCID=%d;Tpt=2;natf=17;natm=1;pmp=0;sid=30;upnp=0", pretHoteLAN, hote.ID))))
	endParticipation(s, mm, hote, gid, 51)
	base := len(cvis.rmc(t, s))

	if resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Fatal("sortie a l'instant d'un salon ouvert : on attend son retour")
	}
	time.Sleep(80 * time.Millisecond)
	var sess MatchmakeSession
	NewStreamIn(mm.ExtensionHandler()(hote, autoMatchmakeReq(s)).Body, s).Extract(&sess)
	if sess.ID != gid {
		t.Fatalf("l'hote doit revenir dans son salon %d, recu %d", gid, sess.ID)
	}
	time.Sleep(220 * time.Millisecond) // au-dela du budget de retour (150 ms depuis la sortie)
	if n := len(reponsesURL(cvis.rmc(t, s)[base:])); n != 0 {
		t.Fatalf("le retour dans le salon doit prolonger l'attente, %d reponse(s) deja partie(s)", n)
	}
	sc(hote, replaceURLReq(s, 28, stationReplaceURLHote(hote.ID, pretHoteCID)))
	msgs := attendreReponseURL(t, s, cvis, base, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("reponse attendue apres le ReplaceURL, recu %d", len(msgs))
	}
	if lan := urlsDe(s, msgs[0])[0]; lan.GetInt("port") != pretHoteUDP || lan.GetInt("CID") != pretHoteCID {
		t.Errorf("station servie = %s, attendu le port %d de la nouvelle session (pas 60541)", lan, pretHoteUDP)
	}
}

// Cible qui sonde deja sans ReplaceURL (37 InitiateProbe sur 37 sans verdict le 15/10) :
// l'InitiateProbe en retour part tout de suite, comme avant.
func TestInitiateProbeVersUneCibleQuiSondeDejaNonRetenu(t *testing.T) {
	j := capterJournalNAT(t)
	s, _, hote, visiteur, chote, _, _ := salonDeProd(t)
	nat := NATTraversalHandler()
	// L'hote, pas pret, sonde le visiteur...
	nat(hote, probeReq(s, 110, visiteur, fmt.Sprintf("prudp:/address=%s;port=%d;RVCID=%d;probeinit=1", pretHoteIP, pretHoteUDP, hote.ID)))
	base := len(chote.rmc(t, s))
	// ... qui lui repond.
	station := fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=0", visiteur.ID)
	nat(visiteur, probeReq(s, 111, hote, station))
	msgs := chote.rmc(t, s)[base:]
	if len(msgs) != 1 || msgs[0].Method != MethodInitiateProbe {
		t.Fatalf("InitiateProbe immediat attendu, recu %d message(s)", len(msgs))
	}
	corps := NewStreamOut(s)
	corps.StationURL(ParseStationURL(station))
	if !bytes.Equal(msgs[0].Encode(), NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+111, corps.Bytes()).Encode()) {
		t.Error("InitiateProbe different de l'ancien envoi")
	}
	if !strings.Contains(j.texte(), "envoye sans retenue") || !strings.Contains(j.texte(), "sonde deja") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
}

// Cible sortie d'un salon sans y revenir : la retenue est coupee a la fin du budget de
// retour, l'InitiateProbe part une seule fois.
func TestInitiateProbeRetenueCoupeeQuandPlusAttendu(t *testing.T) {
	j := capterJournalNAT(t)
	regler(t, natAttenteRetour, 100*time.Millisecond)
	s, mm, hote, visiteur, chote, _, gid := salonDeProd(t)
	SecureConnectionHandler()(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, pretHoteCID)))
	endParticipation(s, mm, hote, gid, 51)
	base := len(chote.rmc(t, s))

	debut := time.Now()
	NATTraversalHandler()(visiteur, probeReq(s, 120, hote, fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=1", visiteur.ID)))
	if n := len(chote.rmc(t, s)) - base; n != 0 {
		t.Fatal("sortie a l'instant : l'InitiateProbe attend son retour")
	}
	msgs := chote.attendre(t, s, base+1, time.Second)[base:]
	if len(msgs) != 1 || msgs[0].Method != MethodInitiateProbe {
		t.Fatalf("InitiateProbe attendu a la fin du budget de retour, recu %d", len(msgs))
	}
	if d := time.Since(debut); d > 400*time.Millisecond {
		t.Errorf("InitiateProbe parti %v apres, attendu le budget de retour (100 ms) plus un tour", d)
	}
	if !strings.Contains(j.texte(), "envoye QUAND MEME apres") || !strings.Contains(j.texte(), "ReplaceURL plus attendu") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
	time.Sleep(250 * time.Millisecond)
	if n := len(chote.rmc(t, s)) - base; n != 1 {
		t.Errorf("InitiateProbe envoye %d fois, attendu 1", n)
	}
}

// Cas du 15/10 23:59:16 : la cible sort, le probe arrive 0,36 s apres, la cible revient
// dans le salon 0,34 s plus tard et fait son ReplaceURL 0,58 s apres. Le retour prolonge la
// retenue, l'InitiateProbe part juste apres la reponse au ReplaceURL.
func TestInitiateProbeRetourDansLeSalonProlongeLaRetenue(t *testing.T) {
	j := capterJournalNAT(t)
	regler(t, natAttenteRetour, 150*time.Millisecond)
	s, mm, hote, visiteur, chote, _, gid := salonDeProd(t)
	sc := SecureConnectionHandler()
	sc(hote, replaceURLReq(s, 27, stationReplaceURLHote(hote.ID, 1)))
	endParticipation(s, mm, hote, gid, 51)
	base := len(chote.rmc(t, s))

	NATTraversalHandler()(visiteur, probeReq(s, 130, hote, fmt.Sprintf("prudp:/address=198.51.100.34;port=63315;RVCID=%d;probeinit=1", visiteur.ID)))
	time.Sleep(80 * time.Millisecond)
	var sess MatchmakeSession
	NewStreamIn(mm.ExtensionHandler()(hote, autoMatchmakeReq(s)).Body, s).Extract(&sess)
	if sess.ID != gid {
		t.Fatalf("la cible doit revenir dans son salon %d, recu %d", gid, sess.ID)
	}
	time.Sleep(220 * time.Millisecond)
	for _, m := range chote.rmc(t, s)[base:] {
		if m.Protocol == ProtocolNATTraversal && m.Method == MethodInitiateProbe {
			t.Fatal("le retour dans le salon doit prolonger la retenue")
		}
	}
	avant := len(chote.rmc(t, s))
	if resp := sc(hote, replaceURLReq(s, 28, stationReplaceURLHote(hote.ID, pretHoteCID))); resp != nil {
		t.Fatal("avec un InitiateProbe retenu, le ReplaceURL envoie lui-meme sa reponse puis le probe")
	}
	msgs := chote.rmc(t, s)[avant:]
	if len(msgs) != 2 || msgs[0].Method != MethodReplaceURL || msgs[1].Protocol != ProtocolNATTraversal || msgs[1].Method != MethodInitiateProbe {
		t.Fatalf("attendu [reponse ReplaceURL, InitiateProbe], recu %d message(s)", len(msgs))
	}
	if !strings.Contains(j.texte(), "LIBERE par son ReplaceURL") {
		t.Errorf("journal attendu, recu :\n%s", j.texte())
	}
}

// answerSessionURLsWhenHostIsPret : un hote devenu pret sans RVCID repassait par l'attente
// du RVCID avec sa propre echeance (jusqu'a 5 s au total). Une seule echeance desormais.
func TestAttenteDuRVCIDNeSeCumulePlus(t *testing.T) {
	capterJournalNAT(t)
	s, mm, hote, visiteur, _, cvis, gid := salonDeProd(t)
	base := len(cvis.rmc(t, s))

	debut := time.Now()
	if resp := mm.MatchMakingHandler()(visiteur, sessionURLsReq(s, gid)); resp != nil {
		t.Fatal("hote pas pret : la reponse doit attendre")
	}
	time.Sleep(hostReplaceURLWait - 700*time.Millisecond)
	// L'hote devient pret avec une station LAN sans RVCID : le pont rend bridgeNoRVCID.
	publique := hote.Stations()[1]
	lan := ParseStationURL(fmt.Sprintf("prudp:/address=%s;port=%d;CID=%d;Pl=3;Tpt=2;natf=17;natm=1;pmp=0;sid=30;upnp=0", pretHoteLAN, pretHoteUDP, pretHoteCID))
	hote.SetStations([]*StationURL{lan, publique})
	if _, status := natBridgeStations(hote.Stations(), mm.PublicStationFirst); status != bridgeNoRVCID {
		t.Fatalf("scene : bridgeNoRVCID attendu, recu %d", status)
	}
	hote.natApresReplaceURL(pretHoteCID)
	attendu := sessionURLsResponse(visiteur, sessionURLsReq(s, gid), hote.Stations()).Body

	msgs := cvis.attendre(t, s, base+1, 2*hostReplaceURLWait)[base:]
	if len(msgs) != 1 {
		t.Fatalf("une reponse attendue, recu %d", len(msgs))
	}
	if d := time.Since(debut); d > hostReplaceURLWait+300*time.Millisecond {
		t.Errorf("reponse %v apres la demande : les deux attentes se cumulent (echeance unique %v)", d, hostReplaceURLWait)
	}
	if !bytes.Equal(msgs[0].Body, attendu) {
		t.Errorf("passe l'echeance, les stations telles quelles : %v", urlsDe(s, msgs[0]))
	}
}
