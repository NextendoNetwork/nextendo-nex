package nex

import "testing"

func TestSecureConnectionRegister(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)

	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800000000
	conn.ID = 42

	// Client reports a LAN url and a public url (both without the Public flag,
	// as the Switch does).
	lan := NewStationURL("prudp")
	lan.Set("address", "10.0.0.5")
	lan.Set("port", "1234")
	pub := NewStationURL("prudp")
	pub.Set("address", "88.1.2.3")
	pub.Set("port", "5678")

	body := NewStreamOut(s)
	WriteList(body, []*StationURL{lan, pub}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	req := NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 3, body.Bytes())

	resp := SecureConnectionHandler()(conn, req)
	if resp.IsError {
		t.Fatalf("register errored: %+v", resp)
	}

	in := NewStreamIn(resp.Body, s)
	retval := in.U32()
	cid := in.U32()
	urlPublic := ParseStationURL(in.String())

	if retval != ResultCoreUnknown {
		t.Fatalf("retval %#x", retval)
	}
	if cid != 42 {
		t.Fatalf("connection id %d", cid)
	}
	if urlPublic.GetInt("type") != 0x0B {
		t.Fatalf("public type %d (want 0x0B)", urlPublic.GetInt("type"))
	}
	if urlPublic.Get("Pa") != "10.0.0.5" {
		t.Fatalf("Pa %q (want the LAN address)", urlPublic.Get("Pa"))
	}
	if urlPublic.GetInt("RVCID") != 42 {
		t.Fatalf("RVCID %d", urlPublic.GetInt("RVCID"))
	}
	if len(conn.Stations()) != 2 {
		t.Fatalf("stored %d station urls", len(conn.Stations()))
	}
}

func TestConnectionRegistry(t *testing.T) {
	ep := NewEndpoint(testSettings())
	c := NewConnection(ep, "1.2.3.4:1", func([]byte) {})

	ep.registerConnection(c)
	if c.ID == 0 || ep.FindConnectionByID(c.ID) != c {
		t.Fatalf("register failed: id=%d", c.ID)
	}
	c.close()
	if ep.FindConnectionByID(c.ID) != nil {
		t.Fatal("connection not removed on close")
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
		{"a player address", false, "a real player's public address"},
		{"", false, "not an address"},
	} {
		if got := isPrivateIP(c.addr); got != c.want {
			t.Errorf("isPrivateIP(%q) = %v, want %v (%s)", c.addr, got, c.want, c.why)
		}
	}
}
