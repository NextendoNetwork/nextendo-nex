package nex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// natTable writes a nat_endpoints.txt holding one observation per IP, the shape the
// NAT responder actually produces, and points the lookup at it.
func natTable(t *testing.T, lines string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nat_endpoints.txt")
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NNCS_NAT_FILE", p)
	natCacheMu.Lock()
	natCache, natCacheRead = nil, time.Time{}
	natCacheMu.Unlock()
}

func TestNATPortRepointedWhenOneConsoleBehindTheIP(t *testing.T) {
	natTable(t, "203.0.113.9 59321\n")
	ep := NewEndpoint(testSettings())
	c := NewConnection(ep, "203.0.113.9:56765", func([]byte) {})
	c.PID, c.ID = 1800009999, 2
	ep.registerConnection(c)

	if !natPortUnambiguous(c, "203.0.113.9") {
		t.Fatal("a single console behind the address must still be repointed")
	}
}

// The case that produced error 2106-0583: host and joiner behind one public IP, the
// table holding only the joiner's port. Repointing there rewrote the HOST's station to
// the JOINER's port, so the joiner probed itself and the punch failed.
func TestNATPortNotRepointedWhenTwoConsolesShareTheIP(t *testing.T) {
	natTable(t, "203.0.113.9 59321\n")
	ep := NewEndpoint(testSettings())

	host := NewConnection(ep, "203.0.113.9:56757", func([]byte) {})
	host.PID, host.ID = 1800003406, 1
	ep.registerConnection(host)

	joiner := NewConnection(ep, "203.0.113.9:56765", func([]byte) {})
	joiner.PID, joiner.ID = 1800009999, 2
	ep.registerConnection(joiner)

	if natPortUnambiguous(host, "203.0.113.9") {
		t.Error("host: shared public IP must not take the table's single port")
	}
	if natPortUnambiguous(joiner, "203.0.113.9") {
		t.Error("joiner: shared public IP must not take the table's single port")
	}
}

// A console that reconnects briefly holds two connections; that is one player, not two,
// and must not disable repointing for itself.
func TestNATPortRepointedAcrossOneConsolesReconnect(t *testing.T) {
	natTable(t, "203.0.113.9 59321\n")
	ep := NewEndpoint(testSettings())

	stale := NewConnection(ep, "203.0.113.9:40001", func([]byte) {})
	stale.PID, stale.ID = 1800009999, 7
	ep.registerConnection(stale)

	fresh := NewConnection(ep, "203.0.113.9:40002", func([]byte) {})
	fresh.PID, fresh.ID = 1800009999, 8
	ep.registerConnection(fresh)

	if !natPortUnambiguous(fresh, "203.0.113.9") {
		t.Error("two connections from one PID are one console")
	}
}
