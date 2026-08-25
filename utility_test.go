package nex

import (
	"testing"
)

// A fresh account asks for a NEX unique id the first time it goes online, keeps it, and
// never asks again — so leaving these unanswered breaks new players only, which is what
// made it look like the servers were fine.
func TestUtilityHandsOutNexUniqueID(t *testing.T) {
	s := testSettings()
	conn := &Connection{Settings: s, PID: 1001}
	h := UtilityHandler()

	t.Run("AcquireNexUniqueID returns the pid", func(t *testing.T) {
		resp := h(conn, NewRMCRequest(s, ProtocolUtility, MethodAcquireNexUniqueID, 1, nil))
		if resp == nil || resp.IsError {
			t.Fatalf("want success, got %+v", resp)
		}
		if got := NewStreamIn(resp.Body, s).U64(); got != 1001 {
			t.Errorf("unique id = %d, want the caller's pid", got)
		}
	})

	t.Run("AcquireNexUniqueIDWithPassword returns id + password", func(t *testing.T) {
		resp := h(conn, NewRMCRequest(s, ProtocolUtility, MethodAcquireNexUniqueIDWithPassword, 2, nil))
		if resp == nil || resp.IsError {
			t.Fatalf("want success, got %+v", resp)
		}

		var info UniqueIDInfo
		NewStreamIn(resp.Body, s).Extract(&info)

		if info.NEXUniqueID != 1001 {
			t.Errorf("unique id = %d, want the caller's pid", info.NEXUniqueID)
		}
		// Stable and derived: the same account must get the same password every time, or a
		// returning player's stored credentials stop matching.
		if want := uint64(1001) ^ nexUniqueIDPasswordSalt; info.NEXUniqueIDPassword != want {
			t.Errorf("password = %d, want %d (pid ^ salt)", info.NEXUniqueIDPassword, want)
		}
	})

	t.Run("the id is stable across calls", func(t *testing.T) {
		a := h(conn, NewRMCRequest(s, ProtocolUtility, MethodAcquireNexUniqueIDWithPassword, 3, nil))
		b := h(conn, NewRMCRequest(s, ProtocolUtility, MethodAcquireNexUniqueIDWithPassword, 4, nil))

		var x, y UniqueIDInfo
		NewStreamIn(a.Body, s).Extract(&x)
		NewStreamIn(b.Body, s).Extract(&y)

		if x != y {
			t.Errorf("id/password must not change between calls: %+v vs %+v", x, y)
		}
	})
}

// The end-of-session transition Splatoon 2 runs after a Salmon Run result. Answering it at
// all is the contract; NotImplemented aborts the finish with 2306-0103.
func TestUpdateMatchmakeSessionPartIsAcknowledged(t *testing.T) {
	s := testSettings()
	conn := &Connection{Settings: s, PID: 1001}
	m := NewMatchmaking()

	resp := m.ExtensionHandler()(conn, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateMatchmakeSessionPart, 1, nil))
	if resp == nil || resp.IsError {
		t.Fatalf("want an empty success, got %+v", resp)
	}
	if len(resp.Body) != 0 {
		t.Errorf("body = % x, want empty", resp.Body)
	}
}

// The host publishes its match configuration here; Smash sends ~422 bytes as soon as a
// lobby forms. Refusing it leaves joiners with a session they cannot interpret.
func TestUpdateApplicationBufferStoresTheHostsBuffer(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	m := NewMatchmaking()
	host := &Connection{Settings: s, Endpoint: ep, PID: 1002}
	ext := m.ExtensionHandler()

	// Host creates a gathering.
	create := NewStreamOut(s)
	create.Add(&MatchmakeSession{GameMode: 1})
	if r := ext(host, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, create.Bytes())); r == nil || r.IsError {
		t.Fatalf("autoMatchmake: %+v", r)
	}

	gid := uint32(1)
	payload := make([]byte, 422)
	for i := range payload {
		payload[i] = byte(i)
	}

	req := NewStreamOut(s)
	req.U32(gid)
	req.Buffer(payload)

	resp := ext(host, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateApplicationBuffer, 2, req.Bytes()))
	if resp == nil || resp.IsError {
		t.Fatalf("the owner must be allowed to publish its buffer, got %+v", resp)
	}

	m.mu.Lock()
	stored := m.gatherings[gid].session.ApplicationData
	m.mu.Unlock()

	if len(stored) != len(payload) || stored[100] != payload[100] {
		t.Errorf("buffer stored as %d bytes, want the host's %d verbatim", len(stored), len(payload))
	}

	// A joiner must not be able to rewrite the host's match.
	joiner := &Connection{Settings: s, Endpoint: ep, PID: 1003}
	if r := ext(joiner, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodUpdateApplicationBuffer, 3, req.Bytes())); r == nil || !r.IsError {
		t.Errorf("a non-owner must be denied, got %+v", r)
	}
}
