package nex

import (
	"strings"
	"testing"
)

// GHSA-j6m5-jqph-f634: a station URL a client could overflow on is never kept nor sent.
func TestStationURLLengthBound(t *testing.T) {
	ok := "prudp:/address=1.2.3.4;port=1234;natf=0;natm=0;pmp=0;sid=15;type=3;upnp=0;PID=1800000123;RVCID=42"
	if got := ParseStationURL(ok).String(); got != ok {
		t.Fatalf("legit URL changed: %q", got)
	}
	long := "prudp:/address=1.2.3.4;x=" + strings.Repeat("A", 2000)
	if u := ParseStationURL(long); u.Has("address") || u.String() != "" {
		t.Fatalf("oversized URL kept: %q", u.String())
	}
	u := NewStationURL("prudp")
	u.Set("k", strings.Repeat("B", MaxStationURLLength))
	if u.String() != "" {
		t.Fatal("oversized URL serialized")
	}
}
