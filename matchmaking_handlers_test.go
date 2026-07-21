package nex

import "testing"

func newTestConn(ep *Endpoint, pid uint64, addr string) *Connection {
	c := NewConnection(ep, addr, func([]byte) {})
	c.PID = pid
	ep.registerConnection(c)
	return c
}

// TestMatchmakingCreateJoinAndURLs proves the pairing core: the first player
// creates a gathering, the second joins the same one, and the second can then
// fetch the host's station URLs (what Pia needs to start the P2P connection).
func TestMatchmakingCreateJoinAndURLs(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext := mm.ExtensionHandler()
	mmh := mm.MatchMakingHandler()

	// Host A (registered, with a station URL as Register would have set).
	connA := newTestConn(ep, 1000, "88.0.0.1:1")
	stA := NewStationURL("prudp")
	stA.Set("address", "88.0.0.1")
	stA.SetInt("RVCID", int(connA.ID))
	connA.SetStations([]*StationURL{stA})

	// Joiner B.
	connB := newTestConn(ep, 2000, "88.0.0.2:1")

	autoReq := func() *RMCMessage {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering:         Gathering{MaxParticipants: 12},
			GameMode:          3,
			OpenParticipation: true,
			Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		}}
		body := NewStreamOut(s)
		body.Add(p)
		return NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, body.Bytes())
	}

	// A creates the gathering.
	var sessA MatchmakeSession
	NewStreamIn(ext(connA, autoReq()).Body, s).Extract(&sessA)
	if sessA.OwnerPID != 1000 || sessA.HostPID != 1000 || sessA.ID == 0 || sessA.NumParticipants != 1 {
		t.Fatalf("A (host) session: %+v", sessA)
	}
	gid := sessA.ID

	// B joins the SAME gathering.
	var sessB MatchmakeSession
	NewStreamIn(ext(connB, autoReq()).Body, s).Extract(&sessB)
	if sessB.ID != gid || sessB.OwnerPID != 1000 || sessB.NumParticipants != 2 {
		t.Fatalf("B session: %+v (want gid=%d host=1000 participants=2)", sessB, gid)
	}

	// B fetches the host's station URLs.
	urlReq := NewStreamOut(s)
	urlReq.U32(gid)
	resp := mmh(connB, NewRMCRequest(s, ProtocolMatchMaking, MethodGetSessionURLs, 2, urlReq.Bytes()))
	urls := ReadList(NewStreamIn(resp.Body, s), func(i *StreamIn) *StationURL { return i.StationURLValue() })
	if len(urls) != 1 || urls[0].GetInt("RVCID") != int(connA.ID) || urls[0].Get("address") != "88.0.0.1" {
		t.Fatalf("host session urls: %+v", urls)
	}
}

// TestMatchmakingDistinctGameModes checks that incompatible modes don't merge.
func TestMatchmakingDistinctGameModes(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext := mm.ExtensionHandler()

	connA := newTestConn(ep, 1, "1.1.1.1:1")
	connB := newTestConn(ep, 2, "2.2.2.2:1")

	req := func(mode uint32) *RMCMessage {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 12}, GameMode: mode, OpenParticipation: true, Attribs: []uint32{0, 0, 0, 0, 0, 0}}}
		b := NewStreamOut(s)
		b.Add(p)
		return NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, b.Bytes())
	}

	var a, b MatchmakeSession
	NewStreamIn(ext(connA, req(3)).Body, s).Extract(&a)
	NewStreamIn(ext(connB, req(7)).Body, s).Extract(&b)
	if a.ID == b.ID {
		t.Fatalf("different game modes must not share a gathering (both gid=%d)", a.ID)
	}
}
