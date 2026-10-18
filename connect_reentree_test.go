package nex

// Garde de re-entree du CONNECT (bloquant de la relecture securite du tour 4) : un CONNECT
// rejoue sur une connexion deja connectee ne cree ni entree, ni goroutine, ni OnConnect ;
// la console recoit toujours son CONNECT-ACK ; un CONNECT sur une connexion fermee ne la
// ressuscite pas. PID fictifs (18009xxxxx), adresses de documentation (203.0.113.0/24).

import (
	"bytes"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// connectValide : un paquet CONNECT accepte par le verificateur des tests (verificateur_test.go)
// pour la signature connSig. check change la charge utile sans changer le PID.
func connectValide(t *testing.T, s *Settings, connSig []byte, pid uint64, check uint32) []byte {
	t.Helper()
	return EncodePacket(connectPacket(s.AccessKey, connSig, chargeConnectTest(s, pid, 0x1234, check)))
}

func entreesConnexions(ep *Endpoint) int {
	ep.connMu.Lock()
	defer ep.connMu.Unlock()
	return len(ep.connections)
}

type temoinConnexion struct {
	ep        *Endpoint
	conn      *Connection
	mu        sync.Mutex
	envoyes   [][]byte
	connectes atomic.Int64
	partis    atomic.Int64
}

func nouveauTemoin(t *testing.T, addr string) (*temoinConnexion, *Packet) {
	s := testSettings()
	tc := &temoinConnexion{ep: NewEndpoint(s)}
	tc.ep.SetSecureAccount("mot-de-passe-de-test", 2)
	tc.ep.OnConnect = func(*Connection) { tc.connectes.Add(1) }
	tc.ep.OnDisconnect = func(*Connection) { tc.partis.Add(1) }
	tc.conn = NewConnection(tc.ep, addr, func(b []byte) {
		tc.mu.Lock()
		tc.envoyes = append(tc.envoyes, append([]byte(nil), b...))
		tc.mu.Unlock()
	})
	tc.conn.Feed(EncodePacket(synPacket()))
	return tc, decodeOne(t, tc.envoyes[0])
}

// acksConnect : CONNECT-ACK envoyes (payload = reponse de connexion).
func (tc *temoinConnexion) acksConnect(t *testing.T) [][]byte {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	var out [][]byte
	for _, b := range tc.envoyes {
		pk, _, err := DecodePackets(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pk {
			if p.Type == PacketCONNECT && p.HasFlag(FlagACK) {
				out = append(out, p.Payload)
			}
		}
	}
	return out
}

func TestConnectRejoueUneSeuleEntree(t *testing.T) {
	tc, synAck := nouveauTemoin(t, "203.0.113.7:40000")
	s := tc.ep.Settings
	pkt := connectValide(t, s, synAck.ConnectionSig, 1800900042, 0xCAFE)
	g0 := runtime.NumGoroutine()
	n := 500
	tc.conn.Feed(bytes.Repeat(pkt, n))
	time.Sleep(50 * time.Millisecond)
	if e := entreesConnexions(tc.ep); e != 1 {
		t.Fatalf("%d CONNECT identiques : %d entrees, attendu 1", n, e)
	}
	if c := tc.connectes.Load(); c != 1 {
		t.Fatalf("OnConnect %d fois, attendu 1", c)
	}
	if g := runtime.NumGoroutine() - g0; g > 3 {
		t.Fatalf("%d goroutines de plus, attendu 1 (retransmitLoop)", g)
	}
	acks := tc.acksConnect(t)
	if len(acks) != n {
		t.Fatalf("%d CONNECT-ACK pour %d CONNECT (la console doit toujours recevoir sa reponse)", len(acks), n)
	}
	for _, a := range acks {
		if !bytes.Equal(a, acks[0]) {
			t.Fatalf("CONNECT-ACK rejoue different : %x / %x", a, acks[0])
		}
	}
	if tc.conn.PID != 1800900042 {
		t.Fatalf("PID %d", tc.conn.PID)
	}

	// Meme PID, autre charge utile (nouvelle requete) : un ACK portant SA reponse, rien d'autre.
	tc.conn.Feed(connectValide(t, s, synAck.ConnectionSig, 1800900042, 0xBEEF))
	acks = tc.acksConnect(t)
	if len(acks) != n+1 || bytes.Equal(acks[n], acks[0]) || entreesConnexions(tc.ep) != 1 || tc.connectes.Load() != 1 {
		t.Fatalf("CONNECT du meme PID avec une autre requete : %d ACK, %d entrees, %d OnConnect", len(acks), entreesConnexions(tc.ep), tc.connectes.Load())
	}
	// Autre PID : ignore (pas d'ACK, le PID ne change pas).
	tc.conn.Feed(connectValide(t, s, synAck.ConnectionSig, 1800900043, 0xCAFE))
	if len(tc.acksConnect(t)) != n+1 || tc.conn.PID != 1800900042 {
		t.Fatalf("CONNECT d'un autre PID accepte (PID %d)", tc.conn.PID)
	}

	tc.conn.Close()
	if e := entreesConnexions(tc.ep); e != 0 {
		t.Fatalf("apres Close : %d entrees, attendu 0", e)
	}
	if tc.partis.Load() != 1 {
		t.Fatalf("OnDisconnect %d fois", tc.partis.Load())
	}
	// CONNECT apres la fermeture : rien (ni entree, ni ACK, ni OnConnect).
	tc.conn.Feed(pkt)
	if entreesConnexions(tc.ep) != 0 || tc.connectes.Load() != 1 || len(tc.acksConnect(t)) != n+1 {
		t.Fatal("un CONNECT sur une connexion fermee l'a ressuscitee")
	}
	if tc.ep.ReapIdle(time.Nanosecond) != 0 {
		t.Fatal("le reaper trouve encore une entree")
	}
}

// Close et CONNECT en meme temps : OnDisconnect jamais avant OnConnect, aucune entree
// restante.
func TestConnectEtCloseConcurrents(t *testing.T) {
	for i := 0; i < 200; i++ {
		tc, synAck := nouveauTemoin(t, "203.0.113.8:40000")
		var ordre []string
		var mu sync.Mutex
		tc.ep.OnConnect = func(*Connection) { mu.Lock(); ordre = append(ordre, "C"); mu.Unlock() }
		tc.ep.OnDisconnect = func(*Connection) { mu.Lock(); ordre = append(ordre, "D"); mu.Unlock() }
		pkt := connectValide(t, tc.ep.Settings, synAck.ConnectionSig, 1800900044, 1)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); tc.conn.Feed(pkt) }()
		go func() { defer wg.Done(); tc.conn.Feed(pkt) }()
		go func() { defer wg.Done(); tc.conn.Close() }()
		wg.Wait()
		tc.conn.Close()
		mu.Lock()
		o := strings.Join(ordre, "")
		mu.Unlock()
		if o != "" && o != "D" && o != "CD" {
			t.Fatalf("ordre des rappels %q", o)
		}
		if e := entreesConnexions(tc.ep); e != 0 {
			t.Fatalf("%d entrees restantes", e)
		}
	}
}

// Par le vrai transport (gws, message d'1 Mio comme main.go) : la memoire ne bouge plus.
func TestConnectRejoueWebSocket(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	ep.SetSecureAccount("mot-de-passe-de-test", 2)
	ep.MaxRMCSize = 1 << 20
	srv := NewServer(ep)
	srv.ReadMaxPayloadSize = 1 << 20
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()
	cli, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial : %v", err)
	}
	defer cli.Close()
	synAckC := make(chan *Packet, 1)
	var lus atomic.Int64
	go func() {
		for {
			_, b, err := cli.ReadMessage()
			if err != nil {
				return
			}
			if lus.Add(1) == 1 {
				if pk, _, _ := DecodePackets(b); len(pk) > 0 {
					synAckC <- pk[0]
				}
			}
		}
	}()
	_ = cli.WriteMessage(websocket.BinaryMessage, EncodePacket(synPacket()))
	var synAck *Packet
	select {
	case synAck = <-synAckC:
	case <-time.After(2 * time.Second):
		t.Fatal("pas de SYN-ACK")
	}
	pkt := connectValide(t, s, synAck.ConnectionSig, 1800900045, 7)
	msg := bytes.Repeat(pkt, (1<<20)/len(pkt))
	runtime.GC()
	g0 := runtime.NumGoroutine()
	for i := 0; i < 8; i++ {
		if err := cli.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for lus.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	if g := runtime.NumGoroutine() - g0; g > 20 {
		t.Fatalf("%d goroutines de plus apres 8 messages de CONNECT rejoues", g)
	}
	if e := entreesConnexions(ep); e != 1 {
		t.Fatalf("%d entrees, attendu 1", e)
	}
}
