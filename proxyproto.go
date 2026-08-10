package nex

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// PROXY-protocol support. When the auth server sits behind a TLS-passthrough proxy
// (the reverse proxy on the shared :443 of the main VPS, routing by SNI to the SSBU server),
// the TCP peer the auth sees is the PROXY, not the console. That breaks the IP-based
// PID inheritance (RememberAuthPID at auth / RecallAuthPID at the direct secure
// connection): the auth would remember the reverse proxy's IP, but the secure connection
// arrives on the console's REAL IP, so the lookup misses and the session gets a
// placeholder PID (the console's own hosted session is then owned by a phantom PID →
// crash). Enabling PROXY protocol on the reverse proxy service makes it prepend the real
// client address, which we parse here so the auth sees the same IP as the secure
// connection. MK8 (direct :443 on a host, no proxy) doesn't need this.

// proxyConn overrides RemoteAddr with the real client address from the PROXY header.
type proxyConn struct {
	net.Conn
	reader     *bufio.Reader
	remoteAddr net.Addr
}

func (c *proxyConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (c *proxyConn) RemoteAddr() net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return c.Conn.RemoteAddr()
}

// proxyListener parses the PROXY-protocol v1 header (a single CRLF-terminated line:
// "PROXY TCP4 <srcIP> <dstIP> <srcPort> <dstPort>") at the start of each accepted
// connection, before TLS. Connections without the header pass through unchanged.
type proxyListener struct{ net.Listener }

func (l *proxyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	pc := &proxyConn{Conn: c, reader: bufio.NewReader(c)}
	if head, _ := pc.reader.Peek(6); string(head) == "PROXY " {
		line, lerr := pc.reader.ReadString('\n')
		if lerr == nil {
			f := strings.Fields(strings.TrimSpace(line))
			// f = [PROXY, TCP4|TCP6|UNKNOWN, srcIP, dstIP, srcPort, dstPort]
			if len(f) >= 6 && (f[1] == "TCP4" || f[1] == "TCP6") {
				port, _ := strconv.Atoi(f[4])
				if ip := net.ParseIP(f[2]); ip != nil {
					pc.remoteAddr = &net.TCPAddr{IP: ip, Port: port}
				}
			}
		}
	}
	return pc, nil
}

// ListenSecureProxy is ListenSecure but behind a PROXY-protocol v1 front (the reverse proxy):
// it reads the real client IP from the PROXY header, then terminates TLS itself.
func (s *Server) ListenSecureProxy(port int, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	tlsLn := tls.NewListener(&proxyListener{ln}, &tls.Config{Certificates: []tls.Certificate{cert}})
	srv := &http.Server{
		Handler:      s.mux(),
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	return srv.Serve(tlsLn)
}
