package nex

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRegisterCoLocatedSingleURL reproduces the Splatoon 2 host shape that failed:
// the client reports ONE station url carrying a VPN-looking address (Hamachi
// 25.x), its connection arrives hairpinned from the router's LAN address. Register
// must store a distinct local/public pair (bridge input) and answer with a public
// station that points at the ALWAYS-LIVE, RESPONDING P2P anchor — the host's own
// reported station (a VPN/LAN address) can never answer the resolve job's probes,
// so the job cannot see a consistent client port and the host dies 2618-201.
func TestRegisterCoLocatedSingleURL(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := newTestConn(ep, 1800002682, "192.168.1.1:61375")

	nat := filepath.Join(t.TempDir(), "nat.txt")
	t.Setenv("NNCS_NAT_FILE", nat)
	if err := os.WriteFile(nat, []byte("192.168.1.1 50601\n90.246.6.248 50601\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LegacyPiaConfig()
	cfg.PublicHost = "90.246.6.248"
	cfg.P2PAnchorPort = 33334

	url := NewStationURL("prudp")
	url.Set("address", "25.42.44.246")
	url.SetInt("port", 1)
	url.SetInt("sid", 30)
	url.Set("Pl", "3")
	url.Set("Tpt", "2")
	url.Set("natf", "0")
	url.Set("natm", "0")
	url.Set("pmp", "0")
	url.Set("upnp", "0")

	body := NewStreamOut(s)
	WriteList(body, []*StationURL{url}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	resp := handleRegister(conn, NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 1, body.Bytes()), cfg)
	if resp == nil {
		t.Fatal("register returned nil")
	}

	st := conn.Stations()
	if len(st) != 2 {
		t.Fatalf("stored stations len=%d (want 2 distinct), got %+v", len(st), st)
	}
	var localAddr string
	publicPort := -1
	for _, u := range st {
		if isPrivateIP(u.Get("address")) {
			localAddr = u.Get("address")
		} else {
			publicPort = u.GetInt("port")
		}
	}
	if localAddr != "192.168.1.1" {
		t.Errorf("stored local = %q, want the hairpin source 192.168.1.1", localAddr)
	}
	if publicPort != 33334 {
		t.Errorf("stored public port = %d, want the P2P anchor 33334 (the host's own station can never answer probes)", publicPort)
	}

	in := NewStreamIn(resp.Body, s)
	in.U32() // retval
	in.U32() // pidConnectionID
	respURL := in.StationURLValue()
	if respURL == nil {
		t.Fatal("register response carries no urlPublic")
	}
	if got := respURL.Get("address"); got != "90.246.6.248" {
		t.Errorf("resp address = %q, want 90.246.6.248 (the anchor the host can actually probe)", got)
	}
	if got := respURL.GetInt("port"); got != 33334 {
		t.Errorf("resp port = %d, want the P2P anchor 33334 (the resolve target must answer)", got)
	}
}

// isPrivateIP decides which url is the LAN candidate, which is the whole local/public
// split the natbridge builds on. Prefix matching got two ranges wrong.
func TestIsPrivateIPUsesRealRanges(t *testing.T) {
	for _, c := range []struct {
		addr string
		want bool
		why  string
	}{
		{"192.168.1.64", true, "RFC1918"},
		{"10.0.0.200", true, "RFC1918"},
		{"172.16.0.1", true, "RFC1918 starts at 172.16"},
		{"172.31.255.254", true, "RFC1918 ends at 172.31"},
		{"172.15.0.1", false, "below the RFC1918 block — public"},
		{"172.217.5.1", false, "Google — the '172.' prefix match called this private"},
		{"100.64.0.1", true, "CGNAT"},
		{"100.127.0.1", true, "CGNAT reaches 100.127 — the '100.64.' prefix missed it"},
		{"100.128.0.1", false, "past CGNAT — public"},
		{"203.0.113.21", false, "a real player's public address"},
		{"", false, "not an address"},
	} {
		if got := isPrivateIP(c.addr); got != c.want {
			t.Errorf("isPrivateIP(%q) = %v, want %v (%s)", c.addr, got, c.want, c.why)
		}
	}
}

// TestRegisterCoLocatedTwoURLs: a host that reports a real private LAN url AND a
// VPN-looking public url. The private local must be kept; only the public is
// repointed.
func TestRegisterCoLocatedTwoURLs(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := newTestConn(ep, 1800002683, "192.168.1.1:61376")

	nat := filepath.Join(t.TempDir(), "nat.txt")
	t.Setenv("NNCS_NAT_FILE", nat)
	if err := os.WriteFile(nat, []byte("192.168.1.1 51700\n90.246.6.248 51700\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LegacyPiaConfig()
	cfg.PublicHost = "90.246.6.248"
	cfg.P2PAnchorPort = 33334

	local := NewStationURL("prudp")
	local.Set("address", "192.168.1.70")
	local.SetInt("port", 1)
	pub := NewStationURL("prudp")
	pub.Set("address", "25.42.44.246")
	pub.SetInt("port", 1)

	body := NewStreamOut(s)
	WriteList(body, []*StationURL{local, pub}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	resp := handleRegister(conn, NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 1, body.Bytes()), cfg)
	if resp == nil {
		t.Fatal("register returned nil")
	}

	var localAddr, publicAddr string
	publicPort := -1
	for _, u := range conn.Stations() {
		if isPrivateIP(u.Get("address")) {
			localAddr = u.Get("address")
		} else {
			publicAddr = u.Get("address")
			publicPort = u.GetInt("port")
		}
	}
	if localAddr != "192.168.1.70" {
		t.Errorf("stored local = %q, want the reported private LAN 192.168.1.70", localAddr)
	}
	if publicAddr != "90.246.6.248" || publicPort != 33334 {
		t.Errorf("stored public = %s:%d, want 90.246.6.248:33334 (the P2P anchor)", publicAddr, publicPort)
	}

	in := NewStreamIn(resp.Body, s)
	in.U32()
	in.U32()
	respURL := in.StationURLValue()
	if respURL == nil || respURL.Get("address") != "90.246.6.248" || respURL.GetInt("port") != 33334 {
		t.Errorf("resp url = %+v, want address 90.246.6.248 port 33334", respURL)
	}
}

// TestRegisterRemoteClient: a remote friend behind a normal private LAN. The
// public station must point at the observed endpoint and the response must match
// it (stock behaviour is preserved for clients the anchor cannot help).
func TestRegisterRemoteClient(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := newTestConn(ep, 1800002684, "88.77.66.55:30000")

	nat := filepath.Join(t.TempDir(), "nat.txt")
	t.Setenv("NNCS_NAT_FILE", nat)
	if err := os.WriteFile(nat, []byte("88.77.66.55 41000\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LegacyPiaConfig()
	cfg.PublicHost = "90.246.6.248"
	cfg.P2PAnchorPort = 33334

	url := NewStationURL("prudp")
	url.Set("address", "192.168.0.10")
	url.SetInt("port", 1)

	body := NewStreamOut(s)
	WriteList(body, []*StationURL{url}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	resp := handleRegister(conn, NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 1, body.Bytes()), cfg)
	if resp == nil {
		t.Fatal("register returned nil")
	}

	var localAddr, publicAddr string
	for _, u := range conn.Stations() {
		if isPrivateIP(u.Get("address")) {
			localAddr = u.Get("address")
		} else {
			publicAddr = u.Get("address")
		}
	}
	if localAddr != "192.168.0.10" {
		t.Errorf("stored local = %q, want the reported LAN 192.168.0.10", localAddr)
	}
	if publicAddr != "88.77.66.55" {
		t.Errorf("stored public = %q, want the observed endpoint", publicAddr)
	}

	in := NewStreamIn(resp.Body, s)
	in.U32()
	in.U32()
	respURL := in.StationURLValue()
	if respURL == nil || respURL.Get("address") != "88.77.66.55" {
		t.Errorf("resp url = %+v, want address 88.77.66.55", respURL)
	}
}
