package nex

// Correctifs de la relecture r2 (securite, coeur), transport WebSocket. Chaque test echouait
// avant son correctif :
//   - TestR2SocketPingSansLecture : PING sans lecture, boucle de lecture bloquee dans le PONG,
//     socket sans CONNECT gardee au-dela de l'echeance (4 sur 4, 5 s apres) ;
//   - TestSocketConnecteeEcritureBornee : meme blocage apres un CONNECT, aucune echeance
//     d'ecriture (socket encore ouverte 6 s apres le blocage).
// Adresses de boucle locale (banc).

import (
	"bufio"
	"fmt"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

// bancSockets : serveur WebSocket de test (httptest, sans TLS) et ouverture brute d'une
// socket dont le tampon de reception est reduit a 4 Kio.
type bancSockets struct {
	t    *testing.T
	srv  *Server
	addr string
}

func nouveauBancSockets(t *testing.T) *bancSockets {
	s := testSettings()
	ep := NewEndpoint(s)
	ep.SetSecureAccount("mot-de-passe-de-test", 2)
	srv := NewServer(ep) // SocketsParIP : 0 -> NEXTENDO_WS_PAR_IP, 32 par defaut
	ts := httptest.NewServer(srv.mux())
	t.Cleanup(ts.Close)
	return &bancSockets{t: t, srv: srv, addr: strings.TrimPrefix(ts.URL, "http://")}
}

func (b *bancSockets) ouvrir() net.Conn {
	c, err := net.Dial("tcp", b.addr)
	if err != nil {
		b.t.Fatalf("dial : %v", err)
	}
	b.t.Cleanup(func() { c.Close() })
	_ = c.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(c)
	ligne, _ := br.ReadString('\n')
	if !strings.Contains(ligne, "101") {
		b.t.Fatalf("upgrade : %q", ligne)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	return c
}

func (b *bancSockets) ouvertes() int {
	n := 0
	b.srv.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

func (b *bancSockets) parIP() int {
	b.srv.ipMu.Lock()
	defer b.srv.ipMu.Unlock()
	n := 0
	for _, v := range b.srv.parIP {
		n += v
	}
	return n
}

// pingSansLire : des PING masques a charge vide (0x89 0x80 + cle de 4 octets), sans jamais
// lire, jusqu'a ce que l'ecriture du client bloque (6 s au plus). Rend vrai si elle a bloque.
func pingSansLire(t *testing.T, c net.Conn, i int) bool {
	ping := []byte{0x89, 0x80, 1, 2, 3, 4}
	lot := []byte(strings.Repeat(string(ping), 20000))
	envoye, debut := 0, time.Now()
	for time.Since(debut) < 6*time.Second {
		_ = c.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		k, err := c.Write(lot)
		envoye += k
		if err != nil {
			ne, ok := err.(net.Error)
			bloque := ok && ne.Timeout()
			if !bloque {
				t.Logf("socket %d : ecriture du client en erreur %v (fermee par le serveur ?)", i, err)
			}
			t.Logf("socket %d : %d octets de PING envoyes en %v, ecriture du client bloquee = %v", i, envoye, time.Since(debut).Round(time.Millisecond), bloque)
			return bloque
		}
	}
	t.Logf("socket %d : %d octets de PING envoyes en %v sans blocage", i, envoye, time.Since(debut).Round(time.Millisecond))
	return false
}

func TestR2SocketPingSansLecture(t *testing.T) {
	ancien := delaiAvantConnect
	delaiAvantConnect = 8 * time.Second
	t.Cleanup(func() { delaiAvantConnect = ancien })
	b := nouveauBancSockets(t)
	runtime.GC()
	g0 := runtime.NumGoroutine()

	const n = 4
	var premiere time.Time
	for i := 0; i < n; i++ {
		c := b.ouvrir()
		if i == 0 {
			premiere = time.Now()
		}
		pingSansLire(t, c, i)
	}
	attente := 5 * time.Second
	time.Sleep(time.Until(premiere.Add(delaiAvantConnect + attente)))
	ouvertes := b.ouvertes()
	t.Logf("%v apres l'echeance d'avant CONNECT de la 1re socket (%v) : %d socket(s) toujours ouvertes cote serveur (Server.conns), %d place(s) prises dans le plafond par IP, %d goroutine(s) de plus",
		attente, delaiAvantConnect, ouvertes, b.parIP(), runtime.NumGoroutine()-g0)
	if ouvertes > 0 || b.parIP() > 0 {
		t.Fatalf("%d socket(s) sans CONNECT gardee(s) au-dela de l'echeance : PING sans lecture, boucle de lecture bloquee dans le PONG", ouvertes)
	}
}

// Apres un CONNECT accepte, chaque PING repousse l'echeance de lecture (2 fois le seuil du
// reaper) : seule l'echeance d'ecriture libere une socket qui ne lit plus.
func TestSocketConnecteeEcritureBornee(t *testing.T) {
	ancienC, ancienE := delaiAvantConnect, delaiEcritureSocket
	delaiAvantConnect, delaiEcritureSocket = time.Minute, time.Second
	t.Cleanup(func() { delaiAvantConnect, delaiEcritureSocket = ancienC, ancienE })
	b := nouveauBancSockets(t)
	c := b.ouvrir()
	// CONNECT accepte, simule : la connexion de la socket est marquee comme apres
	// processCONNECT (OnOpen tourne dans la boucle de lecture, juste apres l'upgrade). Tampon
	// d'envoi du serveur reduit a 4 Kio : sous Linux, celui par defaut (jusqu'a tcp_wmem max)
	// absorbait tous les PONG d'un essai de quelques Mio, l'ecriture du serveur ne bloquait
	// jamais et le test ne mesurait rien.
	limite := time.Now().Add(2 * time.Second)
	marquee := false
	for !marquee && time.Now().Before(limite) {
		b.srv.conns.Range(func(k, v any) bool {
			if tc, ok := k.(*gws.Conn).NetConn().(*net.TCPConn); ok {
				_ = tc.SetWriteBuffer(4096)
			}
			v.(*Connection).connecteUneFois.Store(true)
			marquee = true
			return false
		})
		time.Sleep(10 * time.Millisecond)
	}
	if !marquee {
		t.Fatalf("connexion de la socket introuvable")
	}
	// L'ecriture du client bloque (Windows), ou echoue parce que le serveur a deja ferme la
	// socket (Linux : tampons plus grands, l'echeance d'ecriture du serveur passe avant que
	// ceux du client ne soient pleins). Avant le correctif : bloquee, socket jamais fermee.
	pingSansLire(t, c, 0)
	// Echeance d'ecriture, puis fermeture TLS : sans ecriture en cours, crypto/tls envoie
	// close_notify avec 5 s au plus (Conn.closeNotify) a un client qui ne lit pas. Mesure sur
	// le VPS (golang:1.26.7) : 5,8 a 6,0 s apres le blocage, et un peu plus de 6 s sous la
	// charge du go test complet ; borne du test : echeance + 5 s + 3 s de marge.
	debut := time.Now()
	for b.ouvertes() > 0 && time.Since(debut) < delaiEcritureSocket+8*time.Second {
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("socket connectee qui ne lit plus : %d ouverte(s) %v apres le blocage (echeance d'ecriture %v)", b.ouvertes(), time.Since(debut).Round(time.Millisecond), delaiEcritureSocket)
	if b.ouvertes() > 0 || b.parIP() > 0 {
		t.Fatalf("socket connectee bloquee en ecriture gardee au-dela de l'echeance d'ecriture")
	}
}
