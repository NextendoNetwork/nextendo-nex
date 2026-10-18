package nex

// Relecture Shared Content (mineur du coeur) : une panique dans OnConnect ne doit pas garder
// cycleMu (close() bloquait pour toujours : connexion jamais retiree, lecture figee).
// PID fictifs (18009xxxxx), adresses de documentation (203.0.113.0/24).

import (
	"testing"
	"time"
)

func TestPaniqueOnConnectNeBloquePasClose(t *testing.T) {
	tc, syn := nouveauTemoin(t, "203.0.113.23:6000")
	tc.ep.OnConnect = func(*Connection) { panic("essai") }
	func() {
		defer func() { _ = recover() }()
		tc.conn.Feed(connectValide(t, tc.conn.Settings, syn.ConnectionSig, 1800900023, 1))
	}()
	fini := make(chan struct{})
	go func() {
		tc.conn.Close()
		close(fini)
	}()
	select {
	case <-fini:
	case <-time.After(2 * time.Second):
		t.Fatal("close() bloque apres une panique dans OnConnect (cycleMu jamais rendu)")
	}
	if n := entreesConnexions(tc.ep); n != 0 {
		t.Fatalf("connexion encore enregistree apres close() : %d", n)
	}
}
