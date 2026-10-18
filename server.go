package nex

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lxzan/gws"
)

// Server hosts an Endpoint over WebSocket (the PRUDP-Lite transport). It uses the
// lxzan/gws library because the Switch's NEX WebSocket handshake is non-standard
// — it offers a "NEX" subprotocol and a Sec-WebSocket-Key that strict RFC 6455
// validators (e.g. gorilla) reject — and gws accepts it. Each socket carries one
// Connection.
type Server struct {
	Endpoint *Endpoint
	conns    sync.Map // *gws.Conn -> *Connection

	// CustomHTTPHandler, when set, answers plain HTTP requests on a path other
	// than "/" instead of attempting a WebSocket upgrade. nil by default, so the
	// transport behaves exactly as before for every existing title.
	CustomHTTPHandler func(w http.ResponseWriter, r *http.Request)

	// ReadMaxPayloadSize, si > 0, borne la taille d'un message WebSocket recu : au-dela,
	// gws ferme la socket (1009). Jusqu'a ParallelGolimit (8) messages par socket sont
	// traites a la fois, chacun pouvant aller jusqu'a cette taille. 0 (defaut) : la borne
	// par defaut de gws, 16 Mio, comportement d'avant.
	ReadMaxPayloadSize int

	// SocketsParIP : WebSockets ouvertes en meme temps depuis une meme adresse IP, au plus.
	// 0 : NEXTENDO_WS_PAR_IP, 32 par defaut ; negatif : sans limite. Au-dela, la requete
	// d'upgrade recoit 503 (voir socketsParIP).
	SocketsParIP int

	ipMu   sync.Mutex
	parIP  map[string]int
	ipDe   sync.Map // *gws.Conn -> adresse IP comptee dans parIP
	refusP atomic.Int64
	refusJ atomic.Int64 // derniere ligne de journal des refus (secondes Unix)

	// avantConnect : *gws.Conn -> *time.Timer qui ferme la socket si aucun CONNECT n'a ete
	// accepte dans les delaiAvantConnect qui suivent l'ouverture (arrete par OnClose).
	avantConnect sync.Map
}

// Echeances de lecture des WebSockets.
//
// Avant : une WebSocket ouverte qui n'envoyait plus rien n'avait aucune echeance
// (ReadHeaderTimeout s'arrete a l'upgrade, rien ne posait de SetReadDeadline) et le reaper
// ne la voyait pas (il ne parcourt que les connexions enregistrees par un CONNECT accepte).
// Chacune gardait le tampon de lecture de 64 Kio de gws et une goroutine : relecture r1,
// 1500 sockets inertes = +1500 goroutines et 75,5 Kio par socket, la memoire du conteneur
// atteints vers 3470 sockets, sans compte ni ticket.
//
// Maintenant : un CONNECT accepte dans les delaiAvantConnect qui suivent l'ouverture, sinon
// la socket est fermee (une console envoie SYN puis CONNECT des l'ouverture). Ensuite, chaque
// message ou PING repousse l'echeance de delaiInactiviteSocket() : deux fois le seuil du
// reaper (NEXTENDO_REAP_IDLE_SECONDS, 10 min par defaut), qui a deja evince la connexion
// d'une console restee muette aussi longtemps. Seuil du reaper a 0 (eviction desactivee) :
// aucune echeance apres le CONNECT, comme avant.
//
// Les echeances de LECTURE ne suffisent pas (relecture r2) : PONG, SYN-ACK et ACK partent de
// la boucle de lecture. Un client qui envoie des PING sans jamais lire remplit les tampons
// TCP, la boucle reste bloquee dans l'ecriture et ne relit plus : l'echeance de lecture ne se
// declenche jamais et OnClose n'est jamais appele (4 sockets sur 4 encore ouvertes 5 s apres
// l'echeance). D'ou, en plus : une minuterie qui FERME la socket (net.Conn.Close debloque
// l'ecriture en cours) si aucun CONNECT n'est accepte a l'echeance, et une echeance
// d'ecriture de delaiEcritureSocket avant chaque ecriture (ecrireSocket).
var delaiAvantConnect = 30 * time.Second

// delaiEcritureSocket : une ecriture sur une WebSocket (message ou PONG) bloquee plus
// longtemps echoue ; gws ferme alors la socket (emitError puis writeClose), la boucle de
// lecture sort et OnClose libere la connexion. Une console qui lit ne s'en approche jamais
// (un message RMC fait quelques Kio).
var delaiEcritureSocket = 10 * time.Second

// ecrireSocket : ecriture bornee par delaiEcritureSocket (gws 1.10.0 n'en pose aucune hors
// poignee de main). L'echeance est posee avant la prise du verrou d'ecriture de gws : deux
// ecrivains concurrents posent chacun maintenant + delai, l'ecart reste de l'ordre de
// l'attente du verrou.
func ecrireSocket(socket *gws.Conn, op gws.Opcode, b []byte) error {
	_ = socket.SetWriteDeadline(time.Now().Add(delaiEcritureSocket))
	return socket.WriteMessage(op, b)
}

func delaiInactiviteSocket() time.Duration {
	if d := ReapIdleTimeout(); d > 0 {
		return 2 * d
	}
	return 0
}

// prolongerSocket repousse l'echeance de lecture d'une socket dont la connexion a eu un
// CONNECT accepte (au plus une fois par seconde : un message n'appelle pas SetReadDeadline a
// chaque fois).
func prolongerSocket(socket *gws.Conn, conn *Connection) {
	if !conn.connecteUneFois.Load() {
		return
	}
	maintenant := time.Now()
	if maintenant.UnixNano()-conn.socketProlongee.Load() < int64(time.Second) {
		return
	}
	conn.socketProlongee.Store(maintenant.UnixNano())
	if d := delaiInactiviteSocket(); d > 0 {
		_ = socket.SetReadDeadline(maintenant.Add(d))
	} else {
		_ = socket.SetReadDeadline(time.Time{})
	}
}

// socketsParIP : plafond effectif (0 : sans limite).
func (s *Server) socketsParIP() int {
	n := s.SocketsParIP
	if n == 0 {
		n = 32
		if v, err := strconv.Atoi(os.Getenv("NEXTENDO_WS_PAR_IP")); err == nil && v != 0 {
			n = v
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

func hoteDe(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// prendreIP : une socket de plus pour cette IP, si le plafond le permet.
func (s *Server) prendreIP(ip string, max int) bool {
	s.ipMu.Lock()
	defer s.ipMu.Unlock()
	if s.parIP == nil {
		s.parIP = map[string]int{}
	}
	if max > 0 && s.parIP[ip] >= max {
		return false
	}
	s.parIP[ip]++
	return true
}

func (s *Server) rendreIP(ip string) {
	s.ipMu.Lock()
	defer s.ipMu.Unlock()
	if s.parIP[ip]--; s.parIP[ip] <= 0 {
		delete(s.parIP, ip)
	}
}

// journalRefusIP : une ligne toutes les 10 s au plus, avec le nombre de refus depuis la
// precedente (une IP qui insiste ne remplit pas le journal).
func (s *Server) journalRefusIP(ip string, max int) {
	n := s.refusP.Add(1)
	maintenant := time.Now().Unix()
	if d := s.refusJ.Load(); maintenant-d >= 10 && s.refusJ.CompareAndSwap(d, maintenant) {
		s.refusP.Add(-n)
		fmt.Printf("[WS] upgrade refuse depuis %s : %d sockets ouvertes pour cette IP (%d refus depuis la ligne precedente)\n", ip, max, n)
	}
}

// NewServer wraps an endpoint in a WebSocket transport.
func NewServer(e *Endpoint) *Server { return &Server{Endpoint: e} }

// --- gws.Event implementation ---

// OnOpen creates a Connection for the new socket.
func (s *Server) OnOpen(socket *gws.Conn) {
	conn := NewConnection(s.Endpoint, socket.RemoteAddr().String(), func(b []byte) {
		_ = ecrireSocket(socket, gws.OpcodeBinary, b)
	})
	// Les messages d'une socket sont lus DANS L'ORDRE (ParallelEnabled: false, voir mux) ;
	// seuls les gestionnaires RMC tournent a part, 8 au plus par connexion (lancerRMC).
	conn.rmcEnVol = make(chan struct{}, rmcEnVolMax)
	s.conns.Store(socket, conn)
	_ = socket.SetReadDeadline(time.Now().Add(delaiAvantConnect))
	// Independante de la boucle de lecture : ferme la socket meme si cette boucle est bloquee
	// dans une ecriture.
	s.avantConnect.Store(socket, time.AfterFunc(delaiAvantConnect, func() {
		if !conn.connecteUneFois.Load() {
			_ = socket.NetConn().Close()
		}
	}))
	fmt.Printf("[WS] open from %s\n", socket.RemoteAddr())
}

// OnClose tears down the socket's Connection.
func (s *Server) OnClose(socket *gws.Conn, err error) {
	if v, ok := s.avantConnect.LoadAndDelete(socket); ok {
		v.(*time.Timer).Stop()
	}
	if v, ok := s.conns.LoadAndDelete(socket); ok {
		v.(*Connection).Close()
	}
	if v, ok := s.ipDe.LoadAndDelete(socket); ok {
		s.rendreIP(v.(string))
	}
}

// OnPing answers keep-alive pings.
func (s *Server) OnPing(socket *gws.Conn, payload []byte) {
	_ = ecrireSocket(socket, gws.OpcodePong, nil)
	if v, ok := s.conns.Load(socket); ok {
		prolongerSocket(socket, v.(*Connection))
	}
}

// OnPong is a no-op.
func (s *Server) OnPong(socket *gws.Conn, payload []byte) {}

// OnMessage feeds each binary frame's bytes to the socket's Connection.
func (s *Server) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode != gws.OpcodeBinary {
		return
	}
	if v, ok := s.conns.Load(socket); ok {
		data := append([]byte(nil), message.Bytes()...)
		v.(*Connection).Feed(data)
		prolongerSocket(socket, v.(*Connection))
	}
}

func (s *Server) mux() *http.ServeMux {
	upgrader := gws.NewUpgrader(s, &gws.ServerOption{
		// false : OnMessage est appele dans l'ordre d'arrivee, dans la boucle de lecture de la
		// socket. Avec true, gws traitait jusqu'a 8 messages d'une meme socket en parallele :
		// deux fragments envoyes coup sur coup etaient souvent traites dans l'ordre inverse, la
		// fin d'une requete etait reassemblee avant son debut et la requete abandonnee (« size
		// mismatch ») : 17 requetes en 10 h de prod (dont des 0x73.97 de 2,6 a 2,9 Ko et des
		// logins de 4 Ko), et de 10 a 21 servies sur 300 requetes en 2 fragments envoyees coup
		// sur coup (relecture du tour 4). Le decodage et le reassemblage sont courts ; les
		// gestionnaires RMC, eux, partent dans leur propre goroutine (lancerRMC), comme avant.
		ParallelEnabled: false,
		Recovery:        gws.Recovery,
		ReadBufferSize:  64 * 1024,
		WriteBufferSize: 64 * 1024,
		// 0 garde la valeur par defaut de gws (16 Mio).
		ReadMaxPayloadSize: s.ReadMaxPayloadSize,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" || r.Header.Get("Sec-WebSocket-Key") != "" {
			ip, max := hoteDe(r.RemoteAddr), s.socketsParIP()
			if !s.prendreIP(ip, max) {
				s.journalRefusIP(ip, max)
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			socket, err := upgrader.Upgrade(w, r)
			if err != nil {
				s.rendreIP(ip)
				fmt.Printf("[WS] upgrade failed from %s path=%q proto=%q: %v\n",
					r.RemoteAddr, r.URL.Path, r.Header.Get("Sec-WebSocket-Protocol"), err)
				return
			}
			s.ipDe.Store(socket, ip)
			go socket.ReadLoop()
			return
		}

		if s.CustomHTTPHandler != nil {
			s.CustomHTTPHandler(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// delaiEnTetesHTTP : http.Server.ReadHeaderTimeout des ports TLS. Poignee de main TLS et
// en-tetes HTTP en 20 s au plus (sans borne avant : une connexion qui n'envoyait jamais ses
// en-tetes restait ouverte). Le corps et la WebSocket apres l'upgrade ne sont pas concernes
// (aucun ReadTimeout : net/http retire le delai une fois les en-tetes lus).
const delaiEnTetesHTTP = 20 * time.Second

// delaiInactifHTTP : http.Server.IdleTimeout des ports TLS. Une connexion HTTP keep-alive
// (Spectate, Shared Content, 404) sans nouvelle requete est fermee apres ce delai (sans lui,
// net/http prend ReadTimeout, 0 : jamais). Les WebSockets, detachees de net/http par
// l'upgrade, ne sont pas concernees (voir delaiAvantConnect).
const delaiInactifHTTP = 120 * time.Second

// ListenSecure serves the endpoint on the given port over secure WebSocket.
// HTTP/2 is disabled (empty TLSNextProto) so the TLS ALPN offers only http/1.1:
// NEX needs the HTTP/1.1 Upgrade handshake.
func (s *Server) ListenSecure(port int, certFile, keyFile string) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", port),
		Handler:           s.mux(),
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		ReadHeaderTimeout: delaiEnTetesHTTP,
		IdleTimeout:       delaiInactifHTTP,
	}
	return srv.ListenAndServeTLS(certFile, keyFile)
}

// ListenInsecure serves the endpoint over plain WebSocket (no TLS) — local test.
func (s *Server) ListenInsecure(port int) error {
	return http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), s.mux())
}
