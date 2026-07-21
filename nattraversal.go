package nex

import "fmt"

// NAT traversal protocol (P2P hole-punch coordination).
const (
	ProtocolNATTraversal uint16 = 0x03

	MethodRequestProbeInitiation         uint32 = 0x1
	MethodInitiateProbe                  uint32 = 0x2
	MethodRequestProbeInitiationExt      uint32 = 0x3
	MethodReportNATTraversalResult       uint32 = 0x4
	MethodReportNATProperties            uint32 = 0x5
	MethodReportNATTraversalResultDetail uint32 = 0x7
)

// NATTraversalHandler coordinates the P2P hole-punch. The key method is
// RequestProbeInitiationExt: when a client asks the server to have its peers
// probe it back, we push an InitiateProbe request to each target carrying the
// caller's own station URL, so BOTH ends fire UDP probes and open their NATs at
// the same time. The stock flow opens only the joiner's NAT, leaving the other
// console's Pia waiting ("communication error"). Every other method is a simple
// acknowledgement (the actual hole-punch happens directly between the consoles).
func NATTraversalHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodRequestProbeInitiationExt:
			pushInitiateProbe(conn, req)
			return natAck(conn, req)
		case MethodReportNATTraversalResult, MethodReportNATTraversalResultDetail:
			logNATResult(conn, req)

			return natAck(conn, req)
		case MethodRequestProbeInitiation,
			MethodInitiateProbe,
			MethodReportNATProperties:
			return natAck(conn, req)
		default:
			return notImplemented(conn, ProtocolNATTraversal, req)
		}
	}
}

func natAck(conn *Connection, req *RMCMessage) *RMCMessage {
	return NewRMCSuccess(conn.Settings, ProtocolNATTraversal, req.Method, req.CallID, nil)
}

// pushInitiateProbe relays a server→client InitiateProbe to every target the
// caller named, carrying the caller's own station URL so each target probes back.
func pushInitiateProbe(conn *Connection, req *RMCMessage) {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	targetList := ReadList(in, func(i *StreamIn) string { return i.String() })
	stationToProbe := in.String()
	if in.Err() != nil {
		return
	}

	// The InitiateProbe request body is a single station URL (the caller's).
	probe := NewStreamOut(s)
	probe.StationURL(ParseStationURL(stationToProbe))
	probeBody := probe.Bytes()

	for _, raw := range targetList {
		rvcid := uint32(ParseStationURL(raw).GetInt("RVCID"))
		if rvcid == 0 {
			continue
		}
		target := conn.Endpoint.FindConnectionByID(rvcid)
		if target == nil {
			continue
		}
		// Server-initiated requests use a distinct call-id space.
		target.SendRMC(NewRMCRequest(s, ProtocolNATTraversal, MethodInitiateProbe, 0xFFFF0000+req.CallID, probeBody))
	}
}

// logNATResult records the console's own verdict on the hole-punch.
//
// This is the one place either console tells us whether P2P actually worked, and it was
// being acknowledged and thrown away. Everything else we can see — the session forms, the
// bridge substitutes the right port, the probe is requested — happens BEFORE the punch;
// when a lobby then loops back to matchmaking, this report is the only evidence of why.
// A failure here means the two consoles never reached each other, which is a different
// problem from anything the server answers wrongly.
//
// Wire (from the proven protocol): ReportNATTraversalResult = (u32 cid, bool result,
// u32 rtt); the Detail variant inserts an i32 detail before the rtt.
func logNATResult(conn *Connection, req *RMCMessage) {
	in := NewStreamIn(req.Body, conn.Settings)

	cid := in.U32()
	result := in.Bool()

	detail := int32(0)
	if req.Method == MethodReportNATTraversalResultDetail {
		detail = int32(in.U32())
	}
	rtt := in.U32()

	if in.Err() != nil {
		fmt.Printf("[NAT] pid=%d result report unparsable (% x)\n", conn.PID, req.Body)

		return
	}

	verdict := "FAILED"
	if result {
		verdict = "ok"
	}

	fmt.Printf("[NAT] pid=%d hole-punch to cid=%d %s (rtt=%dms detail=%d)\n",
		conn.PID, cid, verdict, rtt, detail)
}
