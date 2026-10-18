package nex

// Les messages d'une socket sont traites dans l'ordre : une requete RMC en deux fragments
// envoyes coup sur coup (deux messages WebSocket) est toujours reassemblee dans le bon ordre
// et servie. Avant (gws ParallelEnabled), la fin d'une requete etait souvent traitee avant
// son debut : requete abandonnee (« size mismatch ») ; relecture du tour 4 : 10 a 21
// servies sur 300.

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestFragmentsTraitesDansLOrdre(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s) // port non secure : DATA sans CONNECT (comme l'authentification)
	ep.MaxRMCSize = 1 << 20
	const n = 300
	var bonnes, mauvaises atomic.Int32
	attendu := bytes.Repeat([]byte{0x5A}, 2400)
	ep.Register(0x73, func(c *Connection, req *RMCMessage) *RMCMessage {
		if bytes.Equal(req.Body, attendu) {
			bonnes.Add(1)
		} else {
			mauvaises.Add(1)
		}
		return nil
	})
	srv := NewServer(ep)
	srv.ReadMaxPayloadSize = 1 << 20
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()
	cli, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	go func() {
		for {
			if _, _, err := cli.ReadMessage(); err != nil {
				return
			}
		}
	}()
	func() {
		for i := 0; i < n; i++ {
			rmc := NewRMCRequest(s, 0x73, 97, uint32(i+1), attendu).Encode()
			f1 := EncodePacket(fragmentData(rmc[:1300], uint16(2*i+1), 1))
			f2 := EncodePacket(fragmentData(rmc[1300:], uint16(2*i+2), 0))
			if err := cli.WriteMessage(websocket.BinaryMessage, f1); err != nil {
				t.Fatal(err)
			}
			if err := cli.WriteMessage(websocket.BinaryMessage, f2); err != nil {
				t.Fatal(err)
			}
		}
		fin := time.Now().Add(10 * time.Second)
		for bonnes.Load()+mauvaises.Load() < n && time.Now().Before(fin) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
	}()
	// Une requete abandonnee (« size mismatch ») n'atteint jamais le gestionnaire.
	t.Logf("%d requetes en 2 fragments : %d servies intactes, %d alterees, %d perdues", n, bonnes.Load(), mauvaises.Load(), n-bonnes.Load()-mauvaises.Load())
	if bonnes.Load() != n || mauvaises.Load() != 0 {
		t.Fatalf("ordre des fragments non respecte")
	}
}

// Une panique d'un gestionnaire RMC (servi a part par le transport) est recuperee : le
// processus continue (sinon le test lui-meme s'arreterait) et la socket sert la requete
// suivante.
func TestPaniqueGestionnaireTransport(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	var servies atomic.Int32
	ep.Register(0x73, func(c *Connection, req *RMCMessage) *RMCMessage {
		if req.Method == 1 {
			panic("essai")
		}
		servies.Add(1)
		return nil
	})
	ts := httptest.NewServer(NewServer(ep).mux())
	defer ts.Close()
	cli, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	go func() {
		for {
			if _, _, err := cli.ReadMessage(); err != nil {
				return
			}
		}
	}()
	func() {
		for i, m := range []uint32{1, 2} {
			_ = cli.WriteMessage(websocket.BinaryMessage, EncodePacket(dataPacket(NewRMCRequest(s, 0x73, m, uint32(i+1), nil).Encode(), uint16(i+1))))
		}
		fin := time.Now().Add(5 * time.Second)
		for servies.Load() == 0 && time.Now().Before(fin) {
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if servies.Load() != 1 {
		t.Fatalf("servies=%d apres la panique", servies.Load())
	}
}
