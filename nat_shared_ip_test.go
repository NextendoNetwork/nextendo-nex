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

func TestGetSessionURLsKeepsHostsPortBehindSharedIP(t *testing.T) {
	natTable(t, "203.0.113.9 19498\n") // The last observation belongs to the joiner.
	ep := NewEndpoint(testSettings())
	host := NewConnection(ep, "203.0.113.9:19503", func([]byte) {})
	host.PID, host.ID = 1800001206, 3
	host.SetStations([]*StationURL{
		ParseStationURL("prudp:/address=192.168.1.60;port=19568;CID=436879845;RVCID=3"),
		ParseStationURL("prudp:/address=203.0.113.9;port=19503;type=11"),
	})
	ep.registerConnection(host)
	joiner := NewConnection(ep, "203.0.113.9:19483", func([]byte) {})
	joiner.PID, joiner.ID = 1800007103, 4
	ep.registerConnection(joiner)
	mm := NewMatchmaking()
	mm.gatherings[1] = &gathering{hostConnID: host.ID}

	request := NewStreamOut(joiner.Settings)
	request.U32(1)
	response := mm.MatchMakingHandler()(joiner, NewRMCRequest(joiner.Settings, ProtocolMatchMaking, MethodGetSessionURLs, 1, request.Bytes()))
	if response == nil || response.IsError {
		t.Fatalf("GetSessionURLs failed: %+v", response)
	}
	urls := ReadList(NewStreamIn(response.Body, joiner.Settings), func(in *StreamIn) *StationURL { return in.StationURLValue() })
	if len(urls) != 2 {
		t.Fatalf("urls=%v", urls)
	}
	if urls[0].Get("address") != "192.168.1.60" || urls[0].GetInt("port") != 19568 {
		t.Fatalf("joiner was given the wrong host LAN endpoint: %s", urls[0])
	}
	if urls[1].GetInt("port") == 19498 || urls[0].GetInt("CID") != 436879845 {
		t.Fatalf("joiner's observation replaced host identity: %v", urls)
	}
	if host.Stations()[0].GetInt("port") != 19568 {
		t.Fatal("stored host station was mutated")
	}
}

// Consoles in one household must keep their LAN station: probing the shared public
// address needs NAT hairpinning most home routers refuse, and the punch dies rtt=0.
func TestProbeKeepsLANStationBetweenConsolesBehindOneNAT(t *testing.T) {
	ep := NewEndpoint(testSettings())
	host := NewConnection(ep, "71.192.22.246:52052", func([]byte) {})
	host.PID, host.ID = 1800003406, 1
	joiner := NewConnection(ep, "71.192.22.246:52054", func([]byte) {})
	joiner.PID, joiner.ID = 1800009999, 2
	remote := NewConnection(ep, "186.54.129.113:55412", func([]byte) {})
	remote.PID, remote.ID = 1800000414, 3

	if !behindSameNAT(host, joiner) {
		t.Error("same public address must be recognised as one NAT")
	}
	if behindSameNAT(host, remote) {
		t.Error("different public addresses must not be treated as one NAT")
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
