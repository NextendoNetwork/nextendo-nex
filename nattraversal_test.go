package nex

import (
	"strconv"
	"testing"
)

// TestNATTraversalPushInitiateProbe proves the P2P fix: when player A asks the
// server to have peer B probe it back, the server pushes an InitiateProbe request
// to B carrying A's station URL.
func TestNATTraversalPushInitiateProbe(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)

	var bSent [][]byte
	connB := NewConnection(ep, "2.2.2.2:1", func(b []byte) {
		bSent = append(bSent, append([]byte(nil), b...))
	})
	ep.registerConnection(connB)

	connA := NewConnection(ep, "1.1.1.1:1", func([]byte) {})
	ep.registerConnection(connA)

	// A's request: targetList = [B's public URL, carrying B's RVCID], plus A's
	// own station URL to be probed.
	targetURL := NewStationURL("prudp")
	targetURL.Set("address", "2.2.2.2")
	targetURL.SetInt("RVCID", int(connB.ID))
	stationToProbe := "prudp:/address=1.1.1.1;port=1234;RVCID=" + strconv.Itoa(int(connA.ID))

	body := NewStreamOut(s)
	WriteList(body, []string{targetURL.String()}, func(o *StreamOut, u string) { o.String(u) })
	body.String(stationToProbe)
	req := NewRMCRequest(s, ProtocolNATTraversal, MethodRequestProbeInitiationExt, 5, body.Bytes())

	resp := NATTraversalHandler()(connA, req)
	if resp.IsError {
		t.Fatalf("RequestProbeInitiationExt errored: %+v", resp)
	}

	// B must have received exactly one pushed InitiateProbe request.
	if len(bSent) != 1 {
		t.Fatalf("B received %d packets (want 1 push)", len(bSent))
	}
	pkt := decodeOne(t, bSent[0])
	if pkt.Type != PacketDATA {
		t.Fatalf("push is not a DATA packet: %+v", pkt)
	}
	pushed, err := ParseRMC(s, pkt.Payload)
	if err != nil {
		t.Fatalf("parse pushed RMC: %v", err)
	}
	if pushed.Mode != RMCRequest || pushed.Protocol != ProtocolNATTraversal || pushed.Method != MethodInitiateProbe {
		t.Fatalf("pushed RMC: %+v", pushed)
	}
	// The push carries A's station URL for B to probe.
	probed := ParseStationURL(NewStreamIn(pushed.Body, s).String())
	if probed.Get("address") != "1.1.1.1" {
		t.Fatalf("probed url address = %q", probed.Get("address"))
	}
}

// TestNATTraversalAcks checks the ack methods return success.
func TestNATTraversalAcks(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "1.1.1.1:1", func([]byte) {})
	h := NATTraversalHandler()

	for _, m := range []uint32{MethodReportNATProperties, MethodReportNATTraversalResult, MethodReportNATTraversalResultDetail} {
		resp := h(conn, NewRMCRequest(s, ProtocolNATTraversal, m, 1, nil))
		if resp.IsError {
			t.Fatalf("method %#x should ack, got error", m)
		}
	}
}
