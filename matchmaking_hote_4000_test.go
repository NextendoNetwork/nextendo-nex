package nex

import (
	"encoding/hex"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Trames de Nintendo (capture MK8 un outil interne, gid 0x8b32dd), PID des
// joueurs remplaces par des valeurs fictives : 0x0102030405060708 (l'appelant de 23:33:32) et
// 0x1112131415161718 (celui de 23:36:44). Le reste est octet pour octet la capture.
const (
	pidFictifA = 0x0102030405060708
	pidFictifB = 0x1112131415161718
	// 23:33:33.184705 : 4000 a l'appelant, 81 ms apres la reponse vide de 0x15.0x2A.
	nintendo4000 = "0037000000" + "0807060504030201" + "a00f0000" + "dd328b0000000000" + "0807060504030201" +
		"1100" + "3137383635373034313330343233323700" + "0000000000000000"
	// 23:36:44.337648 : 110000 a un autre participant, 1,5 ms apres le 4000.
	nintendo110000 = "0027000000" + "1817161514131211" + "b0ad0100" + "dd328b0000000000" + "0000000000000000" +
		"010000" + "0000000000000000"
)

// TestAvis4000OctetsDeNintendo : les avis que construit updateSessionHost s'ecrivent octet
// pour octet comme ceux de Nintendo (en-tete de Structure, PID, type, param1, param2, texte,
// param3).
func TestAvis4000OctetsDeNintendo(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		nom string
		ev  *NotificationEvent
		hex string
	}{
		{"4000", &NotificationEvent{PIDSource: pidFictifA, Type: NotificationOwnershipChanged, Param1: 0x8b32dd, Param2: pidFictifA, StrParam: "1786570413042327"}, nintendo4000},
		{"110000", &NotificationEvent{PIDSource: pidFictifB, Type: NotificationHostChanged, Param1: 0x8b32dd}, nintendo110000},
	} {
		o := NewStreamOut(s)
		o.Add(cas.ev)
		if got := hex.EncodeToString(o.Bytes()); got != cas.hex {
			t.Fatalf("%s :\n obtenu  %s\n attendu %s", cas.nom, got, cas.hex)
		}
	}
}

// rmcRecus decode les messages RMC qu'une connexion a envoyes (ACK et paquets vides ecartes).
func rmcRecus(t *testing.T, s *Settings, c *capte) []*RMCMessage {
	t.Helper()
	c.mu.Lock()
	trames := append([][]byte(nil), c.trames...)
	c.mu.Unlock()
	var out []*RMCMessage
	for _, f := range trames {
		ps, _, err := DecodePackets(f)
		if err != nil {
			t.Fatalf("trame illisible : %v", err)
		}
		for _, p := range ps {
			if p.Type != PacketDATA || p.HasFlag(FlagACK) || len(p.Payload) == 0 {
				continue
			}
			m, err := ParseRMC(s, p.Payload)
			if err != nil {
				t.Fatalf("RMC illisible : %v", err)
			}
			out = append(out, m)
		}
	}

	return out
}

// recu : un message vu par une console, reponse 0x15.0x2A ou avis.
type recu struct {
	reponse bool
	ev      NotificationEvent
}

func recusDepuis(t *testing.T, s *Settings, c *capte, avant int) []recu {
	t.Helper()
	var out []recu
	for _, m := range rmcRecus(t, s, c)[avant:] {
		switch {
		case m.Protocol == ProtocolMatchMaking && m.Mode == RMCResponse && m.Method == MethodUpdateSessionHost:
			if m.IsError || len(m.Body) != 0 {
				t.Fatalf("reponse 0x15.0x2A : erreur=%v corps=%x", m.IsError, m.Body)
			}
			out = append(out, recu{reponse: true})
		case m.Protocol == ProtocolNotifications:
			var ev NotificationEvent
			in := NewStreamIn(m.Body, s)
			in.Extract(&ev)
			if in.Err() != nil {
				t.Fatalf("avis illisible : %v", in.Err())
			}
			out = append(out, recu{ev: ev})
		}
	}

	return out
}

// salonATrois : A (10) cree un salon de Partie rapide, B (11) et C (12) le rejoignent ; les
// trois connexions sont captees.
func salonATrois(t *testing.T) (*Matchmaking, uint32, [3]*Connection, [3]*capte) {
	t.Helper()
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	mm.Endpoint = ep
	mm.MigrationHote = func(uint64) bool { return true }
	mm.SortieProprietaire = true
	ep.Register(ProtocolMatchMaking, mm.MatchMakingHandler())
	ext := mm.ExtensionHandler()
	var conns [3]*Connection
	var caps [3]*capte
	var gid uint32
	for i := range conns {
		conns[i], caps[i] = connCapte(ep, uint64(10+i), "198.51.100."+strconv.Itoa(10+i)+":1")
		conns[i].state.Store(stateConnected)
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4, Flags: 0xa10}, GameMode: 1, OpenParticipation: true}}
		o := NewStreamOut(s)
		o.Add(p)
		var r MatchmakeSession
		NewStreamIn(ext(conns[i], NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
		if i == 0 {
			gid = r.ID
		} else if r.ID != gid {
			t.Fatalf("joueur %d dans le salon %d, attendu %d", 10+i, r.ID, gid)
		}
	}

	return mm, gid, conns, caps
}

// TestMigrationHoteAvis4000TramesReelles : sequence qui echouait en prod (10 fois sur 11 le
// 17/10) : l'ancien hote A est ENCORE dans le salon quand B, elu par les consoles, envoie
// 0x15.0x2A (trame PRUDP reelle, corps u32 gid + booleen 1 comme 0x24bcb0). B recoit la reponse
// vide PUIS le 4000 a son nom (param1 = gid, param2 = B, texte = heure en us sur 16 chiffres),
// ce qui pose l'octet +0x10 que lit 0x1101a0, et aucun 110000 ; A et C recoivent 4000 puis
// 110000 (param2 = 0, texte vide), comme chez Nintendo.
func TestMigrationHoteAvis4000TramesReelles(t *testing.T) {
	s := testSettings()
	mm, gid, conns, caps := salonATrois(t)
	var avant [3]int
	for i := range caps {
		avant[i] = len(rmcRecus(t, s, caps[i]))
	}
	debut := time.Now().UnixMicro()
	conns[1].Feed(EncodePacket(dataPacket(NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 57, corpsUpdateSessionHost(gid, true)).Encode(), 2)))

	mm.mu.Lock()
	g := mm.gatherings[gid]
	hote, proprio, connHote, tete := g.session.HostPID, g.session.OwnerPID, g.hostConnID, g.participants[0]
	mm.mu.Unlock()
	if hote != 11 || proprio != 11 || connHote != conns[1].ID || tete != 11 {
		t.Fatalf("salon : hote=%d proprietaire=%d conn=%d tete=%d", hote, proprio, connHote, tete)
	}

	b := recusDepuis(t, s, caps[1], avant[1])
	if len(b) != 2 || !b[0].reponse || b[1].reponse {
		t.Fatalf("B (appelant) : attendu reponse puis 4000, obtenu %+v", b)
	}
	e := b[1].ev
	if e.Type != NotificationOwnershipChanged || e.PIDSource != 11 || e.Param1 != uint64(gid) || e.Param2 != 11 || e.Param3 != 0 {
		t.Fatalf("4000 de B : %+v", e)
	}
	us, err := strconv.ParseInt(e.StrParam, 10, 64)
	if err != nil || len(e.StrParam) != 16 || us < debut || us > time.Now().UnixMicro()+1000 {
		t.Fatalf("texte du 4000 %q : pas l'heure Unix en microsecondes (debut %d)", e.StrParam, debut)
	}
	for _, i := range []int{0, 2} {
		r := recusDepuis(t, s, caps[i], avant[i])
		if len(r) != 2 || r[0].reponse || r[1].reponse {
			t.Fatalf("console %d : attendu 4000 puis 110000, obtenu %+v", 10+i, r)
		}
		if r[0].ev != e {
			t.Fatalf("console %d : 4000 %+v different de celui de l'appelant %+v", 10+i, r[0].ev, e)
		}
		h := r[1].ev
		if h.Type != NotificationHostChanged || h.PIDSource != 11 || h.Param1 != uint64(gid) || h.Param2 != 0 || h.StrParam != "" || h.Param3 != 0 {
			t.Fatalf("console %d : 110000 %+v", 10+i, h)
		}
	}

	// L'ancien hote sort ensuite (0x32.0x1) : il n'est plus proprietaire, rien ne repart.
	o := NewStreamOut(s)
	o.U32(gid)
	mm.MatchMakingExtHandler()(conns[0], NewRMCRequest(s, ProtocolMatchMakingExt, MethodEndParticipation, 3, o.Bytes()))
	time.Sleep(50 * time.Millisecond)
	if r := recusDepuis(t, s, caps[1], avant[1]); len(r) != 2 {
		t.Fatalf("depart de l'ancien hote : B a recu %d message(s) de plus", len(r)-2)
	}
	mm.mu.Lock()
	proprio = mm.gatherings[gid].session.OwnerPID
	mm.mu.Unlock()
	if proprio != 11 {
		t.Fatalf("apres le depart de l'ancien hote : proprietaire %d", proprio)
	}
}

// TestMigrationHoteSansProprietaireNiAvis4000 : isMigrateOwner faux (Smash ne l'envoie jamais,
// 0x1100dc et 0xeb5f4 posent 1) : l'hote change, pas le proprietaire, aucun 4000.
func TestMigrationHoteSansProprietaireNiAvis4000(t *testing.T) {
	s := testSettings()
	mm, gid, conns, caps := salonATrois(t)
	avant := len(rmcRecus(t, s, caps[2]))
	mm.MatchMakingHandler()(conns[1], NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 5, corpsUpdateSessionHost(gid, false)))
	mm.mu.Lock()
	h, o := mm.gatherings[gid].session.HostPID, mm.gatherings[gid].session.OwnerPID
	mm.mu.Unlock()
	if h != 11 || o != 10 {
		t.Fatalf("hote=%d proprietaire=%d", h, o)
	}
	r := recusDepuis(t, s, caps[2], avant)
	if len(r) != 1 || r[0].ev.Type != NotificationHostChanged {
		t.Fatalf("C : %+v", r)
	}
}

// TestMigrationHoteHorodatagesCroissants : deux reprises successives du meme salon (B puis C)
// donnent a chaque console des textes strictement croissants : le filtre 0x22de80 garde alors
// le dernier 4000, celui du proprietaire final.
func TestMigrationHoteHorodatagesCroissants(t *testing.T) {
	s := testSettings()
	mm, gid, conns, caps := salonATrois(t)
	avant := len(rmcRecus(t, s, caps[0]))
	for _, i := range []int{1, 2} {
		mm.MatchMakingHandler()(conns[i], NewRMCRequest(s, ProtocolMatchMaking, MethodUpdateSessionHost, 6, corpsUpdateSessionHost(gid, true)))
	}
	var textes []int64
	var dernier uint64
	for _, r := range recusDepuis(t, s, caps[0], avant) {
		if r.ev.Type == NotificationOwnershipChanged {
			v, err := strconv.ParseInt(r.ev.StrParam, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			textes = append(textes, v)
			dernier = r.ev.Param2
		}
	}
	if len(textes) != 2 || textes[1] <= textes[0] || dernier != 12 {
		t.Fatalf("4000 recus par A : textes %v, dernier proprietaire annonce %d", textes, dernier)
	}
}

// TestHorodatage4000Unique : appels simultanes (-race) : valeurs toutes differentes, 16
// chiffres, et chaque appelant voit des valeurs croissantes.
func TestHorodatage4000Unique(t *testing.T) {
	const n, k = 16, 200
	var mu sync.Mutex
	vus := map[string]bool{}
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prec := int64(0)
			for range k {
				h := horodatage4000()
				v, err := strconv.ParseInt(h, 10, 64)
				if err != nil || len(h) != 16 || v <= prec {
					t.Errorf("horodatage %q apres %d", h, prec)
					return
				}
				prec = v
				mu.Lock()
				vus[h] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(vus) != n*k {
		t.Fatalf("%d valeurs distinctes sur %d", len(vus), n*k)
	}
}
