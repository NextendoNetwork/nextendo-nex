package nex

import (
	"bytes"
	"testing"
)

// TestMatchmakeSessionRoundTrip encodes a fully-populated MatchmakeSession and
// decodes it back, proving the hierarchical struct-header framing (Gathering
// base level + MatchmakeSession derived level) and the nested MatchmakeParam.
func TestMatchmakeSessionRoundTrip(t *testing.T) {
	s := testSettings()

	orig := &MatchmakeSession{
		Gathering: Gathering{
			ID: 1234, OwnerPID: 1800000000, HostPID: 1800000001,
			MinParticipants: 2, MaxParticipants: 12,
			ParticipationPolicy: 1, PolicyArgument: 0,
			Flags: 512, State: 1, Description: "race",
		},
		GameMode:              42,
		Attribs:               []uint32{10, 20, 30, 40, 50, 60},
		OpenParticipation:     true,
		MatchmakeSystem:       1,
		ApplicationData:       []byte{0xDE, 0xAD, 0xBE, 0xEF},
		NumParticipants:       1,
		ProgressScore:         100,
		SessionKey:            []byte{0x11, 0x22, 0x33},
		Option:                3,
		Param:                 MatchmakeParam{Params: map[string]Variant{"k": {Type: VariantUint64, Uint: 7}}},
		StartedTime:           MakeDateTime(2026, 7, 13, 18, 30, 0),
		UserPassword:          "pw",
		ReferGID:              99,
		UserPasswordEnabled:   true,
		SystemPasswordEnabled: false,
		Codeword:              "ABCDE",
	}

	out := NewStreamOut(s)
	out.Add(orig)

	var got MatchmakeSession
	in := NewStreamIn(out.Bytes(), s)
	in.Extract(&got)
	if in.Err() != nil {
		t.Fatalf("decode error: %v", in.Err())
	}
	if !in.EOF() {
		t.Fatalf("decode left %d bytes unread", in.Remaining())
	}

	// Base (Gathering) level.
	if got.ID != 1234 || got.OwnerPID != 1800000000 || got.HostPID != 1800000001 ||
		got.MaxParticipants != 12 || got.Flags != 512 || got.Description != "race" {
		t.Fatalf("gathering level: %+v", got.Gathering)
	}
	// Derived (MatchmakeSession) level.
	if got.GameMode != 42 || len(got.Attribs) != 6 || got.Attribs[1] != 20 ||
		!got.OpenParticipation || !bytes.Equal(got.ApplicationData, orig.ApplicationData) ||
		got.ProgressScore != 100 || !bytes.Equal(got.SessionKey, orig.SessionKey) ||
		got.Option != 3 || got.StartedTime != orig.StartedTime ||
		got.UserPassword != "pw" || got.ReferGID != 99 || !got.UserPasswordEnabled ||
		got.Codeword != "ABCDE" {
		t.Fatalf("matchmake level: %+v", got)
	}
	// Nested MatchmakeParam.
	if v, ok := got.Param.Params["k"]; !ok || v.Type != VariantUint64 || v.Uint != 7 {
		t.Fatalf("nested param: %+v", got.Param)
	}
}

// TestAutoMatchmakeParamDecode proves the session can be pulled out of an
// AutoMatchmakeParam (a structure that nests a MatchmakeSession).
func TestAutoMatchmakeParamDecode(t *testing.T) {
	s := testSettings()

	orig := &AutoMatchmakeParam{Session: MatchmakeSession{
		Gathering: Gathering{ID: 55, MaxParticipants: 8},
		GameMode:  7,
		Attribs:   []uint32{1, 2, 3, 4, 5, 6},
	}}

	out := NewStreamOut(s)
	out.Add(orig)

	var got AutoMatchmakeParam
	NewStreamIn(out.Bytes(), s).Extract(&got)
	if got.Session.GameMode != 7 || got.Session.ID != 55 || got.Session.MaxParticipants != 8 {
		t.Fatalf("auto param session: %+v", got.Session)
	}
}
