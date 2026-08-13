package nex

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"

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
}

// NewServer wraps an endpoint in a WebSocket transport.
func NewServer(e *Endpoint) *Server { return &Server{Endpoint: e} }

// --- gws.Event implementation ---

// OnOpen creates a Connection for the new socket.
func (s *Server) OnOpen(socket *gws.Conn) {
	conn := NewConnection(s.Endpoint, socket.RemoteAddr().String(), func(b []byte) {
		_ = socket.WriteMessage(gws.OpcodeBinary, b)
	})
	s.conns.Store(socket, conn)
	fmt.Printf("[WS] open from %s\n", socket.RemoteAddr())
}

// OnClose tears down the socket's Connection.
func (s *Server) OnClose(socket *gws.Conn, err error) {
	if v, ok := s.conns.LoadAndDelete(socket); ok {
		v.(*Connection).Close()
	}
}

// OnPing answers keep-alive pings.
func (s *Server) OnPing(socket *gws.Conn, payload []byte) { _ = socket.WritePong(nil) }

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
	}
}

func (s *Server) mux() *http.ServeMux {
	upgrader := gws.NewUpgrader(s, &gws.ServerOption{
		ParallelEnabled: true,
		Recovery:        gws.Recovery,
		ReadBufferSize:  64 * 1024,
		WriteBufferSize: 64 * 1024,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && s.CustomHTTPHandler != nil {
			s.CustomHTTPHandler(w, r)
			return
		}
		socket, err := upgrader.Upgrade(w, r)
		if err != nil {
			fmt.Printf("[WS] upgrade failed from %s path=%q proto=%q: %v\n",
				r.RemoteAddr, r.URL.Path, r.Header.Get("Sec-WebSocket-Protocol"), err)
			return
		}
		go socket.ReadLoop()
	})
	return mux
}

// ListenSecure serves the endpoint on the given port over secure WebSocket.
// HTTP/2 is disabled (empty TLSNextProto) so the TLS ALPN offers only http/1.1:
// NEX needs the HTTP/1.1 Upgrade handshake.
func (s *Server) ListenSecure(port int, certFile, keyFile string) error {
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      s.mux(),
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	return srv.ListenAndServeTLS(certFile, keyFile)
}

// ListenInsecure serves the endpoint over plain WebSocket (no TLS) — local test.
func (s *Server) ListenInsecure(port int) error {
	return http.ListenAndServe(fmt.Sprintf(":%d", port), s.mux())
}
