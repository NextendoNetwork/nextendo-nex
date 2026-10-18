package nex

// Correctifs de la relecture r1 (coeur), transport WebSocket. Echouaient avant le correctif :
// TestSocketsSansConnectFermees (300 sockets muettes apres l'upgrade : 0 fermee, 300
// goroutines gardees) et TestSocketsParIPPlafond (aucun plafond par IP).
// TestSocketConnecteeGardee : une socket avec CONNECT accepte n'est pas touchee.

import (
	"bufio"
	"fmt"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func ouvrirSocketBrute(t *testing.T, addr string) (net.Conn, string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial : %v", err)
	}
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	ligne, _ := bufio.NewReader(c).ReadString('\n')
	return c, strings.TrimSpace(ligne)
}

// Sockets qui se taisent apres l'upgrade : fermees par le serveur faute de CONNECT.
func TestSocketsSansConnectFermees(t *testing.T) {
	ancien := delaiAvantConnect
	delaiAvantConnect = 300 * time.Millisecond
	t.Cleanup(func() { delaiAvantConnect = ancien })
	s := testSettings()
	ep := NewEndpoint(s)
	ep.SetSecureAccount("mot-de-passe-de-test", 2)
	srv := NewServer(ep)
	srv.SocketsParIP = -1
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")

	const n = 300
	runtime.GC()
	g0 := runtime.NumGoroutine()
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < n; i++ {
		c, l := ouvrirSocketBrute(t, addr)
		if !strings.Contains(l, "101") {
			t.Fatalf("upgrade %d : %q", i, l)
		}
		conns = append(conns, c)
	}
	fermees := 0
	limite := time.Now().Add(3 * time.Second)
	for _, c := range conns {
		_ = c.SetReadDeadline(limite)
		b := make([]byte, 64)
		for {
			if _, err := c.Read(b); err != nil {
				if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
					fermees++
				}
				break
			}
		}
	}
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	g := runtime.NumGoroutine() - g0
	t.Logf("%d WebSockets sans CONNECT : %d fermees par le serveur, %d goroutines restantes", n, fermees, g)
	if fermees != n {
		t.Fatalf("%d socket(s) sur %d gardee(s) sans CONNECT au-dela de %v", n-fermees, n, delaiAvantConnect)
	}
	if g > 20 {
		t.Fatalf("%d goroutines restantes", g)
	}
}

// Une socket avec CONNECT accepte n'est pas fermee par l'echeance d'avant CONNECT.
func TestSocketConnecteeGardee(t *testing.T) {
	ancien := delaiAvantConnect
	delaiAvantConnect = 300 * time.Millisecond
	t.Cleanup(func() { delaiAvantConnect = ancien })
	s := testSettings()
	ep := NewEndpoint(s)
	ep.SetSecureAccount("mot-de-passe-de-test", 2)
	ts := httptest.NewServer(NewServer(ep).mux())
	defer ts.Close()
	cli, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial : %v", err)
	}
	defer cli.Close()
	synAckC := make(chan *Packet, 1)
	var lus atomic.Int64
	fin := make(chan error, 1)
	go func() {
		for {
			_, b, err := cli.ReadMessage()
			if err != nil {
				fin <- err
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
	_ = cli.WriteMessage(websocket.BinaryMessage, connectValide(t, s, synAck.ConnectionSig, 1800900045, 7))
	select {
	case err := <-fin:
		t.Fatalf("socket fermee apres le CONNECT : %v", err)
	case <-time.After(4 * delaiAvantConnect):
	}
	if e := entreesConnexions(ep); e != 1 {
		t.Fatalf("%d connexion(s) enregistree(s) apres %v, attendu 1", e, 4*delaiAvantConnect)
	}
}

// Plafond de sockets par IP : la suivante recoit 503, une place rendue se reprend.
func TestSocketsParIPPlafond(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	srv := NewServer(ep)
	srv.SocketsParIP = 3
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		c, l := ouvrirSocketBrute(t, addr)
		if !strings.Contains(l, "101") {
			t.Fatalf("socket %d : %q", i+1, l)
		}
		conns = append(conns, c)
	}
	c, l := ouvrirSocketBrute(t, addr)
	c.Close()
	if !strings.Contains(l, "503") {
		t.Fatalf("4e socket de la meme IP : %q, attendu 503", l)
	}
	conns[0].Close()
	conns = conns[1:]
	for debut := time.Now(); ; {
		c, l := ouvrirSocketBrute(t, addr)
		if strings.Contains(l, "101") {
			conns = append(conns, c)
			break
		}
		c.Close()
		if time.Since(debut) > 3*time.Second {
			t.Fatalf("place non rendue apres la fermeture d'une socket : %q", l)
		}
		time.Sleep(20 * time.Millisecond)
	}
	srv.ipMu.Lock()
	n := srv.parIP["127.0.0.1"]
	srv.ipMu.Unlock()
	if n != 3 {
		t.Fatalf("compteur de l'IP : %d, attendu 3", n)
	}
}
