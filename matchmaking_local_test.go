package nex

import (
	"reflect"
	"testing"
	"time"
)

func localTestStations(udpPort, publicPort int) []*StationURL {
	lan := ParseStationURL("prudp:/address=192.168.1.42;CID=123;RVCID=1;natf=17;natm=1")
	pub := ParseStationURL("prudp:/address=127.0.0.1;type=11;Pa=127.0.0.1;RVCID=1")
	lan.SetInt("port", udpPort)
	pub.SetInt("port", publicPort)
	return []*StationURL{lan, pub}
}

func TestLocalStationsKeepEachPlayersIdentity(t *testing.T) {
	writeNatFile(t, "127.0.0.1 51101\n")
	mm := NewMatchmaking()
	mm.LocalLoopbackStations = true
	for _, ports := range [][2]int{{50835, 63054}, {52690, 58469}} {
		input := localTestStations(ports[0], ports[1])
		before := []string{input[0].String(), input[1].String()}
		out, status := mm.bridgeSessionStations(input)
		if status != bridgeOK || len(out) != 2 {
			t.Fatalf("bridge status=%v, count=%d", status, len(out))
		}
		if out[0].GetInt("port") != ports[0] || out[1].GetInt("port") != ports[1] {
			t.Fatal("UDP destination or public identity was replaced")
		}
		if out[0].Has("type") || out[0].Has("Pa") || out[0].GetInt("CID") != 123 {
			t.Fatal("LAN candidate lost its connection identity")
		}
		if !reflect.DeepEqual(before, []string{input[0].String(), input[1].String()}) {
			t.Fatal("registered stations were mutated")
		}
	}
}

func TestLocalPolicyLeavesOtherModesUnchanged(t *testing.T) {
	writeNatFile(t, "203.0.113.9 40822\n127.0.0.1 51101\n")
	for _, tc := range []struct {
		name                           string
		enabled, publicFirst, loopback bool
	}{
		{"default-local", false, false, true},
		{"default-remote", false, false, false},
		{"enabled-remote", true, false, false},
		{"public-first", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := hostStations()
			if tc.loopback {
				input = localTestStations(50835, 63054)
			}
			want, wantStatus := natBridgeStations(input, tc.publicFirst)
			mm := NewMatchmaking()
			mm.LocalLoopbackStations = tc.enabled
			mm.PublicStationFirst = tc.publicFirst
			got, status := mm.bridgeSessionStations(input)
			if status != wantStatus || len(got) != len(want) {
				t.Fatal("default bridge result changed")
			}
			for i := range got {
				if got[i].String() != want[i].String() {
					t.Fatal("default station changed")
				}
			}
		})
	}
}

func TestLocalGetSessionURLsMatchesRegister(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	host := newTestConn(ep, 1001, "127.0.0.1:63054")
	guest := newTestConn(ep, 1002, "127.0.0.1:58469")
	secure := SecureConnectionHandler()
	lan := ParseStationURL("prudp:/address=192.168.1.42;port=1;Pl=3;Tpt=2;sid=30")
	register := NewStreamOut(s)
	WriteList(register, []*StationURL{lan}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	response := secure(host, NewRMCRequest(s, ProtocolSecureConnection, MethodRegister, 1, register.Bytes()))
	in := NewStreamIn(response.Body, s)
	in.U32()
	in.U32()
	public := in.StationURLValue()
	mm := NewMatchmaking()
	mm.LocalLoopbackStations = true
	if _, status := mm.bridgeSessionStations(host.Stations()); status != bridgeNoRVCID {
		t.Fatal("must wait for ReplaceURL")
	}
	updated := lan.Copy()
	updated.SetInt("port", 50835)
	updated.SetInt("CID", 123)
	updated.SetInt("RVCID", int(host.ID))
	replace := NewStreamOut(s)
	replace.StationURL(lan)
	replace.StationURL(updated)
	secure(host, NewRMCRequest(s, ProtocolSecureConnection, MethodReplaceURL, 2, replace.Bytes()))
	mm.gatherings[1] = &gathering{hostConnID: host.ID}
	request := NewStreamOut(s)
	request.U32(1)
	response = mm.MatchMakingHandler()(guest, NewRMCRequest(s, ProtocolMatchMaking, MethodGetSessionURLs, 3, request.Bytes()))
	if response == nil {
		t.Fatal("ready host should be answered immediately")
	}
	urls := ReadList(NewStreamIn(response.Body, s), func(in *StreamIn) *StationURL { return in.StationURLValue() })
	if len(urls) != 2 || urls[0].GetInt("port") != 50835 || urls[1].GetInt("port") != public.GetInt("port") {
		t.Fatal("GetSessionURLs no longer agrees with Register")
	}
}

func TestParticipationDelayIsPerMatchmaking(t *testing.T) {
	for _, delay := range []time.Duration{0, 60 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			s := testSettings()
			ep := NewEndpoint(s)
			sent := make(chan struct{}, 16)
			c := NewConnection(ep, "127.0.0.1:12345", func([]byte) { sent <- struct{}{} })
			c.PID = 1001
			ep.registerConnection(c)
			mm := NewMatchmaking()
			mm.ParticipationNotificationDelay = delay
			body := NewStreamOut(s)
			body.Add(&AutoMatchmakeParam{Session: MatchmakeSession{Gathering: Gathering{MaxParticipants: 4}}})
			response := mm.ExtensionHandler()(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodCreateMatchmakeSessionWithParam, 1, body.Bytes()))
			if response == nil || response.IsError {
				t.Fatal("create failed")
			}
			if delay == 0 {
				select {
				case <-sent:
				default:
					t.Fatal("default notification must stay synchronous")
				}
			} else {
				select {
				case <-sent:
					t.Fatal("notification arrived before create returned")
				default:
				}
				select {
				case <-sent:
				case <-time.After(time.Second):
					t.Fatal("notification never arrived")
				}
			}
			if NewMatchmaking().ParticipationNotificationDelay != 0 {
				t.Fatal("delay leaked into another server")
			}
		})
	}
}

func TestDelayedParticipationDropsRemovedRoom(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	sent := make(chan struct{}, 16)
	c := NewConnection(ep, "127.0.0.1:12345", func([]byte) { sent <- struct{}{} })
	c.PID = 1001
	ep.registerConnection(c)
	mm := NewMatchmaking()
	mm.ParticipationNotificationDelay = 30 * time.Millisecond
	mm.gatherings[1] = &gathering{participants: []uint64{c.PID}}
	mm.notifyParticipationWithDelay(c, []uint64{c.PID}, 1)
	mm.mu.Lock()
	delete(mm.gatherings, 1)
	mm.mu.Unlock()
	select {
	case <-sent:
		t.Fatal("notification was sent for a removed room")
	case <-time.After(100 * time.Millisecond):
	}
}
