package nex

import "testing"

func registerPiaIdentity(t *testing.T, id uint32, pid uint64, port string, preserve bool) (*Connection, RMCHandler) {
	t.Helper()
	c := &Connection{Settings: testSettings(), ID: id, PID: pid, RemoteAddr: "203.0.113.9:" + port}
	cfg := SwitchPia519Config()
	cfg.PreservePiaStationIdentity = preserve
	h := SecureConnectionHandlerWithConfig(cfg)
	in := NewStreamOut(c.Settings)
	in.U32(1)
	in.StationURL(ParseStationURL("prudp:/address=10.0.0.123;port=1;natf=0;natm=0"))
	h(c, &RMCMessage{Method: MethodRegister, Body: in.Bytes()})
	return c, h
}

func replacePiaIdentity(c *Connection, h RMCHandler, raw string) {
	in := NewStreamOut(c.Settings)
	in.StationURL(c.Stations()[1])
	in.StationURL(ParseStationURL(raw))
	h(c, &RMCMessage{Method: MethodReplaceURL, Body: in.Bytes()})
}

func TestPiaIdentitySurvivesSharedIPObservationAndRoomRecreation(t *testing.T) {
	// Regression from the captured MPS failure: the observation now belongs to
	// the joiner, but the host's identity must still match its own mesh update.
	writeNatFile(t, "203.0.113.9 58073\n")
	host, h := registerPiaIdentity(t, 23, 1800003406, "60249", true)
	mm := NewMatchmaking()
	mm.PreservePiaStationIdentity = true
	if _, status := mm.bridgeSessionStations(host.Stations()); status != bridgeNoRVCID {
		t.Fatal("must wait for the host's CID, even though Register already assigned an RVCID")
	}
	replacePiaIdentity(host, h, "prudp:/address=203.0.113.9;port=60161;CID=3326355617;RVCID=23;natf=34;natm=1")
	previous := host.Stations()
	check := func(privatePort, cid int) {
		t.Helper()
		urls, status := mm.bridgeSessionStations(host.Stations())
		if status != bridgeOK || len(urls) != 2 {
			t.Fatalf("unexpected result: %v %v", urls, status)
		}
		if urls[0].Get("address") != "10.0.0.123" || urls[0].GetInt("port") != privatePort ||
			urls[1].Get("address") != "203.0.113.9" || urls[1].GetInt("port") != 60249 {
			t.Fatalf("identity differs from host's Pia roster: %s / %s", urls[0], urls[1])
		}
		for _, u := range urls {
			if u.GetInt("CID") != cid || u.GetInt("RVCID") != 23 || u.GetInt("PID") != 1800003406 || u.GetInt("natf") != 34 || u.GetInt("natm") != 1 {
				t.Fatalf("lost per-host metadata: %s", u)
			}
		}
		if urls[0].Get("type") != "" || urls[1].GetInt("type") != 11 {
			t.Fatal("private/public classification changed")
		}
	}
	check(60161, 3326355617)
	joiner, jh := registerPiaIdentity(t, 24, 1800009999, "60252", true)
	replacePiaIdentity(joiner, jh, "prudp:/address=203.0.113.9;port=58073;CID=3417268799;RVCID=24;natf=34;natm=1")
	check(60161, 3326355617)
	replacePiaIdentity(host, h, "prudp:/address=203.0.113.9;port=60162;CID=12345;RVCID=23;natf=34;natm=1")
	check(60162, 12345)
	if previous[0].GetInt("port") != 60161 || previous[1].GetInt("CID") != 3326355617 {
		t.Fatal("ReplaceURL mutated a snapshot another connection may be reading")
	}
}

func TestPiaIdentityOptInKeepsExistingBridgeBehavior(t *testing.T) {
	writeNatFile(t, "203.0.113.9 58073\n")
	c, h := registerPiaIdentity(t, 23, 1001, "60249", false)
	replacePiaIdentity(c, h, "prudp:/address=203.0.113.9;port=60161;CID=12345;RVCID=23;natf=34;natm=1")
	urls, status := NewMatchmaking().bridgeSessionStations(c.Stations())
	if status != bridgeOK || urls[0].GetInt("port") != 58073 || urls[1].GetInt("port") != 58073 {
		t.Fatal("default bridge behavior changed")
	}
}
