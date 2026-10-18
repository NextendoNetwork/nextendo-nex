package nex

// Port secure : garde de la DATA avant CONNECT, bornes de taille Endpoint.MaxRMCSize
// (reassemblage des fragments) et Server.ReadMaxPayloadSize (message WebSocket). A 0, valeur
// par defaut des bornes, rien ne change : les cas « temoin » ci-dessous le mesurent.
// TestDataAvantConnectIgnoreeSurPortSecure n'emploie que des symboles du coeur 1e2d015 :
// pose sur ce commit, il echoue.

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fragmentData(payload []byte, packetID uint16, fragmentID uint8) *Packet {
	p := dataPacket(payload, packetID)
	p.FragmentID = fragmentID

	return p
}

func TestReassemblageBorneParMaxRMCSize(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		nom   string
		borne int
	}{{"temoin sans borne (avant)", 0}, {"borne 64 Kio", 64 << 10}} {
		t.Run(cas.nom, func(t *testing.T) {
			ep := NewEndpoint(s)
			ep.MaxRMCSize = cas.borne
			var appels atomic.Int32
			ep.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
				appels.Add(1)

				return NewRMCSuccess(c.Settings, req.Protocol, req.Method, req.CallID, nil)
			})
			conn := NewConnection(ep, "1.2.3.4:5678", func([]byte) {})
			conn.state.Store(stateConnected)

			// 200 fragments de 1300 octets sans fin de message (FragmentID != 0).
			morceau := bytes.Repeat([]byte{0x41}, 1300)
			maxBuf := 0
			for i := 0; i < 200; i++ {
				conn.Feed(EncodePacket(fragmentData(morceau, uint16(i+1), uint8(1+i%250))))
				maxBuf = max(maxBuf, len(conn.fragBuf))
			}
			conn.Feed(EncodePacket(fragmentData(morceau, 201, 0))) // dernier fragment
			t.Logf("tampon de reassemblage : %d octets au plus", maxBuf)
			if cas.borne == 0 {
				if maxBuf != 200*1300 {
					t.Fatalf("sans borne, le tampon doit garder tout (%d octets), il en a garde %d", 200*1300, maxBuf)
				}

				return
			}
			if maxBuf > cas.borne {
				t.Fatalf("tampon de %d octets, borne %d", maxBuf, cas.borne)
			}
			if appels.Load() != 0 {
				t.Fatal("la requete trop grosse ne doit atteindre aucun gestionnaire")
			}
			// La requete suivante, normale, est servie : la connexion reste utilisable.
			conn.Feed(EncodePacket(dataPacket(NewRMCRequest(s, 0x6D, 1, 9, []byte{1}).Encode(), 300)))
			if appels.Load() != 1 {
				t.Fatal("la requete suivante doit etre servie")
			}
			// Une requete en plusieurs fragments sous la borne est servie.
			req := NewRMCRequest(s, 0x6D, 1, 10, bytes.Repeat([]byte{7}, 3000)).Encode()
			conn.Feed(EncodePacket(fragmentData(req[:1300], 301, 1)))
			conn.Feed(EncodePacket(fragmentData(req[1300:2600], 302, 2)))
			conn.Feed(EncodePacket(fragmentData(req[2600:], 303, 0)))
			if appels.Load() != 2 {
				t.Fatal("une requete de 3 fragments sous la borne doit etre servie")
			}
		})
	}
}

// Server.ReadMaxPayloadSize : au-dela, gws ferme la socket ; en dessous, rien ne change.
// Temoin a 0 : la borne par defaut de gws (16 Mio) laisse passer le meme message.
func TestServerReadMaxPayloadSize(t *testing.T) {
	s := testSettings()
	for _, cas := range []struct {
		nom    string
		borne  int
		ferme  bool
		taille int
	}{
		{"temoin sans borne : 128 Kio passe", 0, false, 128 << 10},
		{"borne 64 Kio : 32 Kio passe", 64 << 10, false, 32 << 10},
		{"borne 64 Kio : 128 Kio ferme la socket", 64 << 10, true, 128 << 10},
	} {
		t.Run(cas.nom, func(t *testing.T) {
			srv := NewServer(NewEndpoint(s))
			srv.ReadMaxPayloadSize = cas.borne
			ts := httptest.NewServer(srv.mux())
			defer ts.Close()
			cli, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
			if err != nil {
				t.Fatalf("dial : %v", err)
			}
			defer cli.Close()

			// Octets quelconques : sans borne, le coeur ne sait pas les decoder et les jette,
			// la socket reste ouverte.
			if err := cli.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte{0xFF}, cas.taille)); err != nil {
				if cas.ferme {
					return // fermee par le serveur des l'en-tete du message
				}
				t.Fatalf("envoi : %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			// Un SYN ensuite : SYN-ACK si la socket est ouverte.
			_ = cli.WriteMessage(websocket.BinaryMessage, EncodePacket(synPacket()))
			_ = cli.SetReadDeadline(time.Now().Add(time.Second))
			_, data, err := cli.ReadMessage()
			ouverte := err == nil && len(data) > 0
			if cas.ferme && ouverte {
				t.Fatal("message au-dela de la borne : la socket doit etre fermee")
			}
			if !cas.ferme && !ouverte {
				t.Fatalf("message sous la borne : la socket doit rester ouverte (%v)", err)
			}
		})
	}
}

// Port secure : une DATA recue avant tout CONNECT accepte n'atteint plus aucun gestionnaire
// (avant : servie sous le PID 0). Apres un CONNECT accepte, servie comme avant. Le port
// non secure (authentification) et une connexion fermee par le reaper : inchanges.
func TestDataAvantConnectIgnoreeSurPortSecure(t *testing.T) {
	s := testSettings()
	const (
		userPID   = uint64(1800000000)
		securePID = uint64(2)
	)
	securePassword := "mot-de-passe-de-test"

	ep := NewEndpoint(s)
	ep.SetSecureAccount(securePassword, securePID)
	var appels atomic.Int32
	ep.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
		appels.Add(1)

		return NewRMCSuccess(c.Settings, req.Protocol, req.Method, req.CallID, []byte{0xAA})
	})
	rmc := NewRMCRequest(s, 0x6D, 1, 42, []byte{1, 2}).Encode()

	var sent [][]byte
	conn := NewConnection(ep, "5.6.7.8:1234", func(b []byte) { sent = append(sent, append([]byte(nil), b...)) })
	conn.Feed(EncodePacket(dataPacket(rmc, 1)))
	if n := appels.Load(); n != 0 {
		t.Fatalf("DATA sans CONNECT sur le port secure : gestionnaire appele %d fois (PID %d)", n, conn.PID)
	}
	if len(sent) != 0 {
		t.Fatalf("DATA sans CONNECT : %d paquet(s) renvoye(s), attendu aucun", len(sent))
	}
	// SYN sans CONNECT : toujours rien.
	conn.Feed(EncodePacket(synPacket()))
	synAck := decodeOne(t, sent[0])
	conn.Feed(EncodePacket(dataPacket(rmc, 2)))
	if appels.Load() != 0 {
		t.Fatal("DATA apres SYN mais sans CONNECT : gestionnaire appele")
	}
	// CONNECT refuse (charge illisible) : toujours rien.
	conn.Feed(EncodePacket(connectPacket(s.AccessKey, synAck.ConnectionSig, []byte{1, 2, 3})))
	conn.Feed(EncodePacket(dataPacket(rmc, 3)))
	if appels.Load() != 0 {
		t.Fatal("DATA apres un CONNECT refuse : gestionnaire appele")
	}

	// CONNECT accepte (verificateur des tests) : servie.
	conn.Feed(EncodePacket(connectPacket(s.AccessKey, synAck.ConnectionSig, chargeConnectTest(s, userPID, 0x1234, 0xCAFE))))
	if conn.PID != userPID {
		t.Fatalf("CONNECT accepte : pid=%d", conn.PID)
	}
	avant := len(sent)
	conn.Feed(EncodePacket(dataPacket(rmc, 4)))
	if appels.Load() != 1 || len(sent) != avant+2 {
		t.Fatalf("apres CONNECT : %d appel(s), %d paquet(s) ; attendu 1 appel, ACK + reponse", appels.Load(), len(sent)-avant)
	}

	// Connexion fermee (reaper, kick) : servie comme avant.
	conn.Close()
	conn.Feed(EncodePacket(dataPacket(rmc, 5)))
	if appels.Load() != 2 {
		t.Error("connexion fermee : la DATA doit etre servie comme avant")
	}

	// Port non secure : DATA avant CONNECT servie comme avant.
	auth := NewEndpoint(s)
	var appelsAuth atomic.Int32
	auth.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
		appelsAuth.Add(1)

		return nil
	})
	ca := NewConnection(auth, "5.6.7.8:1235", func([]byte) {})
	ca.Feed(EncodePacket(dataPacket(rmc, 1)))
	if appelsAuth.Load() != 1 {
		t.Error("port non secure : la DATA avant CONNECT doit etre servie comme avant")
	}
}

// Port secure : une connexion fermee SANS avoir jamais ete connectee (DISCONNECT recu, ou
// socket fermee par le transport, avant tout CONNECT accepte) ne doit pas plus servir de DATA
// qu'avant sa fermeture. Avant le correctif, close() passait l'etat a « fermee », la garde
// ne visait que l'etat « en connexion » : SYN, DISCONNECT puis DATA faisait executer le
// gestionnaire sous le PID 0. Temoin : une connexion connectee puis fermee, servie comme avant.
func TestDataApresFermetureSansConnectIgnoree(t *testing.T) {
	s := testSettings()
	const (
		userPID   = uint64(1800900010)
		securePID = uint64(2)
	)
	securePassword := "mot-de-passe-de-test"
	ep := NewEndpoint(s)
	ep.SetSecureAccount(securePassword, securePID)
	var appels atomic.Int32
	ep.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
		appels.Add(1)

		return NewRMCSuccess(c.Settings, req.Protocol, req.Method, req.CallID, []byte{0xAA})
	})
	rmc := NewRMCRequest(s, 0x6D, 1, 42, []byte{1, 2}).Encode()
	disconnect := func() *Packet {
		p := synPacket()
		p.Type, p.Flags, p.PacketID = PacketDISCONNECT, FlagNeedACK, 7

		return p
	}
	ticket := func() []byte {
		return chargeConnectTest(s, userPID, 0x1234, 0xCAFE)
	}

	for _, cas := range []struct {
		nom     string
		etapes  func(conn *Connection, sent *[][]byte)
		servies int32
	}{
		{"SYN, DISCONNECT puis DATA", func(conn *Connection, _ *[][]byte) {
			conn.Feed(EncodePacket(synPacket()))
			conn.Feed(EncodePacket(disconnect()))
		}, 0},
		{"DISCONNECT seul puis DATA", func(conn *Connection, _ *[][]byte) {
			conn.Feed(EncodePacket(disconnect()))
		}, 0},
		{"socket fermee par le transport puis DATA", func(conn *Connection, _ *[][]byte) {
			conn.Feed(EncodePacket(synPacket()))
			conn.Close()
		}, 0},
		{"CONNECT refuse, DISCONNECT puis DATA", func(conn *Connection, sent *[][]byte) {
			conn.Feed(EncodePacket(synPacket()))
			synAck := decodeOne(t, (*sent)[0])
			conn.Feed(EncodePacket(connectPacket(s.AccessKey, synAck.ConnectionSig, []byte{1, 2, 3})))
			conn.Feed(EncodePacket(disconnect()))
		}, 0},
		{"temoin : connectee, DISCONNECT puis DATA (servie comme avant)", func(conn *Connection, sent *[][]byte) {
			conn.Feed(EncodePacket(synPacket()))
			synAck := decodeOne(t, (*sent)[0])
			conn.Feed(EncodePacket(connectPacket(s.AccessKey, synAck.ConnectionSig, ticket())))
			if conn.PID != userPID {
				t.Fatalf("CONNECT accepte : pid=%d", conn.PID)
			}
			conn.Feed(EncodePacket(disconnect()))
		}, 1},
	} {
		t.Run(cas.nom, func(t *testing.T) {
			appels.Store(0)
			var sent [][]byte
			conn := NewConnection(ep, "192.0.2.40:1240", func(b []byte) { sent = append(sent, append([]byte(nil), b...)) })
			cas.etapes(conn, &sent)
			avant := len(sent)
			conn.Feed(EncodePacket(dataPacket(rmc, 9)))
			if n := appels.Load(); n != cas.servies {
				t.Fatalf("gestionnaire appele %d fois (PID %d), attendu %d", n, conn.PID, cas.servies)
			}
			if cas.servies == 0 && len(sent) != avant {
				t.Fatalf("%d paquet(s) renvoye(s) a la DATA, attendu aucun", len(sent)-avant)
			}
		})
	}
}
