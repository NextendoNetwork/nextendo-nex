package nex

import (
	"os"
	"path/filepath"
	"testing"
)

// writeNatFile lays down an nncs-style observation file and points the bridge at it.
func writeNatFile(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nat.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NNCS_NAT_FILE", p)

	// The reader caches for 2s; drop it so each test sees its own file.
	natCacheMu.Lock()
	natCache = nil
	natCacheMu.Unlock()
}

// hostStations is what a console reports: a LAN url carrying the RVCID (from ReplaceURL)
// and a public url whose port is the WebSocket TCP port — useless for a UDP probe.
func hostStations() []*StationURL {
	lan := NewStationURL("prudp")
	lan.Set("address", "192.168.1.42")
	lan.SetInt("port", 12345)
	lan.SetInt("RVCID", 777)

	pub := NewStationURL("prudp")
	pub.Set("address", "203.0.113.9")
	pub.SetInt("port", 54321) // TCP, not the UDP endpoint Pia needs
	pub.SetInt("type", int(StationURLFlagBehindNAT|StationURLFlagPublic))

	return []*StationURL{lan, pub}
}

func TestNatBridgeSubstitutesObservedUdpPort(t *testing.T) {
	writeNatFile(t, "203.0.113.9 40822\n198.51.100.1 1234\n")

	out, status := natBridgeStations(hostStations(), false)
	if status != bridgeOK {
		t.Fatalf("bridge should have substituted, status=%v", status)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 candidates (LAN + public), got %d", len(out))
	}

	lan, pub := out[0], out[1]

	// Both candidates must carry the OBSERVED udp port, not the registered TCP one.
	if lan.GetInt("port") != 40822 || pub.GetInt("port") != 40822 {
		t.Errorf("udp port not substituted: lan=%d pub=%d, want 40822", lan.GetInt("port"), pub.GetInt("port"))
	}

	// The LAN candidate must look local, and must NOT look public: a "type" or "Pa" here
	// makes the console see no LAN candidate at all, fall through to MatchMakingExt m=1
	// and stall — the exact worldwide failure this bridge fixes.
	if lan.Get("address") != "192.168.1.42" {
		t.Errorf("lan address = %q, want the host's private address", lan.Get("address"))
	}
	if lan.Has("type") || lan.Has("Pa") {
		t.Errorf("lan candidate must carry neither type nor Pa, got type=%q Pa=%q", lan.Get("type"), lan.Get("Pa"))
	}
	if lan.GetInt("RVCID") != 777 {
		t.Errorf("lan RVCID = %d, want the id the host reported at ReplaceURL", lan.GetInt("RVCID"))
	}

	// The public candidate keeps the public address and points back at the private one.
	if pub.Get("address") != "203.0.113.9" {
		t.Errorf("pub address = %q, want the host's public address", pub.Get("address"))
	}
	if pub.Get("Pa") != "192.168.1.42" {
		t.Errorf("pub Pa = %q, want the private address", pub.Get("Pa"))
	}
	if got := pub.GetInt("type"); got != 11 {
		t.Errorf("pub type = %d, want 11 (BehindNAT|Public|Switch)", got)
	}
}

// A "type=" left in the key order serialises differently from an absent key. Removing a
// param must drop it from the wire form too, or the LAN candidate still reads as public.
func TestRemovedParamsAreAbsentFromTheWireForm(t *testing.T) {
	writeNatFile(t, "203.0.113.9 40822\n")

	bridged, _ := natBridgeStations(hostStations(), false)
	lan := bridged[0].String()

	for _, bad := range []string{"type=", "Pa="} {
		if contains(lan, bad) {
			t.Errorf("lan url still serialises %q: %s", bad, lan)
		}
	}
}

// Every giving-up path must hand back the original urls: a wrong port is a broken match,
// while the raw urls are at worst the old behaviour.
func TestNatBridgeFallsBackToRawUrls(t *testing.T) {
	t.Run("no observation for this ip", func(t *testing.T) {
		writeNatFile(t, "198.51.100.1 1234\n")
		in := hostStations()
		if out, _ := natBridgeStations(in, false); out[1].GetInt("port") != 54321 {
			t.Errorf("unobserved host must keep its registered urls")
		}
	})

	t.Run("nat file missing", func(t *testing.T) {
		t.Setenv("NNCS_NAT_FILE", filepath.Join(t.TempDir(), "absent.txt"))
		natCacheMu.Lock()
		natCache = nil
		natCacheMu.Unlock()

		if out, _ := natBridgeStations(hostStations(), false); out[1].GetInt("port") != 54321 {
			t.Errorf("a missing nat file must not change the urls")
		}
	})

	t.Run("host has not reported its RVCID yet", func(t *testing.T) {
		writeNatFile(t, "203.0.113.9 40822\n")
		in := hostStations()
		in[0].Set("RVCID", "0") // ReplaceURL not received yet
		if out, _ := natBridgeStations(in, false); out[1].GetInt("port") != 54321 {
			t.Errorf("without an RVCID the joiner cannot probe; urls must be left alone")
		}
	})

	t.Run("lan-only host", func(t *testing.T) {
		writeNatFile(t, "203.0.113.9 40822\n")
		lan := NewStationURL("prudp")
		lan.Set("address", "192.168.1.42")
		in := []*StationURL{lan}
		if out, _ := natBridgeStations(in, false); len(out) != 1 {
			t.Errorf("a host with no public url must be left alone")
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}

	return false
}
