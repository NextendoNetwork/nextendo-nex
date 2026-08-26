package nex

import "testing"

// [Nextendo] Proves the NAT-aware matchmaking decision logic: how a client's reported
// natMap/natFilter buckets into open/moderate/strict, and which pairs are refused so a
// symmetric-NAT ("strict") player can't be dropped into a lobby it can't hole-punch with.
func TestClassifyNAT(t *testing.T) {
	cases := []struct {
		natMap, natFilter uint32
		want              natClass
	}{
		{0, 0, natUnknown},  // not reported yet
		{1, 0, natOpen},     // endpoint-independent mapping + filter
		{1, 1, natOpen},     //
		{1, 2, natModerate}, // filter tighter than mapping
		{0, 2, natModerate}, // map<=1 but filter high
		{2, 1, natStrict},   // address/port-dependent mapping = symmetric-ish
		{2, 2, natStrict},   //
		{3, 3, natStrict},   //
	}
	for _, c := range cases {
		if got := classifyNAT(c.natMap, c.natFilter); got != c.want {
			t.Errorf("classifyNAT(%d,%d) = %s, want %s", c.natMap, c.natFilter, got, c.want)
		}
	}
}

func TestNATCompatible(t *testing.T) {
	// The only combinations that must be REFUSED are the ones that reliably fail to
	// hole-punch: strict×strict and strict×moderate. Unknown is always optimistic.
	compat := map[[2]natClass]bool{
		{natUnknown, natStrict}:    true, // fail-open: don't isolate before we know
		{natStrict, natUnknown}:    true,
		{natOpen, natStrict}:       true, // an open NAT can still reach a symmetric one
		{natStrict, natOpen}:       true,
		{natOpen, natOpen}:         true,
		{natOpen, natModerate}:     true,
		{natModerate, natModerate}: true,
		{natStrict, natModerate}:   false, // <- the poisoning pairs
		{natModerate, natStrict}:   false,
		{natStrict, natStrict}:     false,
	}
	for pair, want := range compat {
		if got := natCompatible(pair[0], pair[1]); got != want {
			t.Errorf("natCompatible(%s,%s) = %v, want %v", pair[0], pair[1], got, want)
		}
	}
}

// A strict joiner must be refused from a lobby containing any strict OR moderate peer, but a
// lobby of only open peers (or with unknown members) is fine. gatheringNATCompatible is what
// autoMatchmake uses to skip a poisoned lobby; here we exercise it without an Endpoint by
// classifying inline via natCompatible (the same predicate it calls per member).
func TestStrictJoinerRefusesPoisonedLobby(t *testing.T) {
	lobby := []natClass{natOpen, natModerate, natOpen} // has a moderate member
	joiner := natStrict
	ok := true
	for _, m := range lobby {
		if !natCompatible(joiner, m) {
			ok = false
			break
		}
	}
	if ok {
		t.Fatal("strict joiner should be refused from a lobby containing a moderate peer")
	}

	openOnly := []natClass{natOpen, natOpen}
	ok = true
	for _, m := range openOnly {
		if !natCompatible(joiner, m) {
			ok = false
			break
		}
	}
	if !ok {
		t.Fatal("strict joiner should be allowed into an all-open lobby")
	}
}
