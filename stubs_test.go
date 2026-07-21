package nex

import "testing"

func TestUtilityAndRankingStubs(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "1.1.1.1:1", func([]byte) {})

	// Utility settings → an empty map (u32 count 0).
	u := UtilityHandler()(conn, NewRMCRequest(s, ProtocolUtility, MethodGetIntegerSettings, 1, nil))
	if u.IsError || len(u.Body) != 4 || NewStreamIn(u.Body, s).U32() != 0 {
		t.Fatalf("utility integer settings: %+v", u)
	}

	// Ranking UploadCommonData → empty success.
	r := RankingHandler()(conn, NewRMCRequest(s, ProtocolRanking, MethodUploadCommonData, 2, []byte{1, 2, 3}))
	if r.IsError || len(r.Body) != 0 {
		t.Fatalf("ranking upload: %+v", r)
	}

	// Ranking worldwide (0x16) → empty list (u32 count 0).
	w := RankingHandler()(conn, NewRMCRequest(s, ProtocolRanking, methodRankingWorldwide, 3, nil))
	if w.IsError || len(w.Body) != 4 {
		t.Fatalf("ranking worldwide: %+v", w)
	}
}
