package nex

import (
	"strconv"
	"testing"
)

func registerStations(t *testing.T, conn *Connection, urls ...*StationURL) *StationURL {
	t.Helper()
	s := conn.Settings
	body := NewStreamOut(s)
	WriteList(body, urls, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	resp := SecureConnectionHandler()(conn, NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 3, body.Bytes()))
	if resp.IsError {
		t.Fatalf("register errored: %+v", resp)
	}
	in := NewStreamIn(resp.Body, s)
	in.U32()
	in.U32()
	return ParseStationURL(in.String())
}

func radminStation() *StationURL {
	u := NewStationURL("prudp")
	u.Set("address", "26.199.81.39")
	u.SetInt("port", 1)
	return u
}

func TestRegisterVPNAdapterAddressDerivesObservedPublic(t *testing.T) {
	ep := NewEndpoint(testSettings())
	conn := NewConnection(ep, "181.42.191.57:24091", func([]byte) {})
	conn.PID = 1800028954
	conn.ID = 41

	pub := registerStations(t, conn, radminStation())
	if pub.Get("address") != "181.42.191.57" {
		t.Fatalf("public url address = %q, want the observed endpoint", pub.Get("address"))
	}

	local, public := selectStations(conn.Stations())
	if local.Get("address") != "26.199.81.39" || public.Get("address") != "181.42.191.57" {
		t.Fatalf("split local=%q public=%q", local.Get("address"), public.Get("address"))
	}
	if local.Has("type") || local.Has("Pa") {
		t.Errorf("local station must not be marked public: %s", local.String())
	}
}

func TestRegisterRealPublicAddressUnchanged(t *testing.T) {
	ep := NewEndpoint(testSettings())
	conn := NewConnection(ep, "158.140.162.138:1", func([]byte) {})
	conn.ID = 7

	lan := NewStationURL("prudp")
	lan.Set("address", "192.168.1.5")
	reported := NewStationURL("prudp")
	reported.Set("address", "158.140.162.216")

	if pub := registerStations(t, conn, lan, reported); pub.Get("address") != "158.140.162.216" {
		t.Fatalf("reported public address replaced with %q", pub.Get("address"))
	}
	if overlayStation(conn, "158.140.162.216", "158.140.162.138") {
		t.Fatal("a reported public address in a CGNAT pool is not an overlay address")
	}
}

func TestNatBridgeVPNAdapterHostAfterReplaceURL(t *testing.T) {
	writeNatFile(t, "181.42.191.57 24416\n")
	ep := NewEndpoint(testSettings())
	conn := NewConnection(ep, "181.42.191.57:24091", func([]byte) {})
	conn.PID = 1800028954
	conn.ID = 41
	registerStations(t, conn, radminStation())

	s := conn.Settings
	repl := NewStationURL("prudp")
	repl.Set("address", "26.199.81.39")
	repl.SetInt("port", 24255)
	repl.SetInt("RVCID", 41)
	repl.SetInt("natf", 1)
	body := NewStreamOut(s)
	body.StationURL(radminStation())
	body.StationURL(repl)
	if resp := SecureConnectionHandler()(conn, NewRMCRequest(s, ProtocolSecureConnection, MethodReplaceURL, 4, body.Bytes())); resp.IsError {
		t.Fatalf("replace url errored: %+v", resp)
	}

	out, status := natBridgeStations(conn.Stations(), false)
	if status != bridgeOK {
		t.Fatalf("bridge status=%v", status)
	}
	if out[0].Get("address") != "26.199.81.39" {
		t.Errorf("lan candidate = %q", out[0].Get("address"))
	}
	if out[1].Get("address") != "181.42.191.57" || out[1].GetInt("port") != 24416 {
		t.Errorf("public candidate = %s, want 181.42.191.57:24416", out[1].String())
	}
}

func TestPushInitiateProbeRepointsVPNAdapterStation(t *testing.T) {
	writeNatFile(t, "181.42.191.57 24416\n")
	s := testSettings()
	ep := NewEndpoint(s)

	var bSent [][]byte
	connB := NewConnection(ep, "2.2.2.2:1", func(b []byte) {
		bSent = append(bSent, append([]byte(nil), b...))
	})
	ep.registerConnection(connB)

	connA := NewConnection(ep, "181.42.191.57:24091", func([]byte) {})
	ep.registerConnection(connA)
	registerStations(t, connA, radminStation())

	targetURL := NewStationURL("prudp")
	targetURL.Set("address", "2.2.2.2")
	targetURL.SetInt("RVCID", int(connB.ID))
	station := "prudp:/address=26.199.81.39;port=24255;RVCID=" + strconv.Itoa(int(connA.ID))

	body := NewStreamOut(s)
	WriteList(body, []string{targetURL.String()}, func(o *StreamOut, u string) { o.String(u) })
	body.String(station)
	if resp := NATTraversalHandler()(connA, NewRMCRequest(s, ProtocolNATTraversal, MethodRequestProbeInitiationExt, 5, body.Bytes())); resp.IsError {
		t.Fatalf("RequestProbeInitiationExt errored: %+v", resp)
	}

	if len(bSent) != 1 {
		t.Fatalf("B received %d packets (want 1 push)", len(bSent))
	}
	pushed, err := ParseRMC(s, decodeOne(t, bSent[0]).Payload)
	if err != nil {
		t.Fatalf("parse pushed RMC: %v", err)
	}
	probed := ParseStationURL(NewStreamIn(pushed.Body, s).String())
	if probed.Get("address") != "181.42.191.57" || probed.GetInt("port") != 24416 {
		t.Fatalf("probed station = %s, want 181.42.191.57:24416", probed.String())
	}
}
