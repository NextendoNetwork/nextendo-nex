package nex

import (
	"testing"
	"time"
)

// A host that has not yet sent its ReplaceURL: public url present, LAN url still the
// port=1 placeholder Register leaves, and no RVCID.
func hostBeforeReplaceURL() []*StationURL {
	lan := NewStationURL("prudp")
	lan.Set("address", "192.168.1.6")
	lan.SetInt("port", 1)

	pub := NewStationURL("prudp")
	pub.Set("address", "203.0.113.9")
	pub.SetInt("port", 47261)
	pub.SetInt("type", int(StationURLFlagBehindNAT|StationURLFlagPublic))

	return []*StationURL{lan, pub}
}

// ...and the same host once it has.
func hostAfterReplaceURL() []*StationURL {
	urls := hostBeforeReplaceURL()
	urls[0].SetInt("port", 49122)
	urls[0].SetInt("RVCID", 264)

	return urls
}

func TestBridgeReportsWhetherItCouldSubstitute(t *testing.T) {
	writeNatFile(t, "203.0.113.9 49275\n")

	if _, status := natBridgeStations(hostBeforeReplaceURL(), false); status != bridgeNoRVCID {
		t.Error("a host with no RVCID must report bridgeNoRVCID so the caller waits for it")
	}

	urls, status := natBridgeStations(hostAfterReplaceURL(), false)
	if status != bridgeOK {
		t.Fatal("a host that reported its ReplaceURL must be bridgeable")
	}
	if urls[1].GetInt("port") != 49275 {
		t.Errorf("public port = %d, want the observed udp port 49275", urls[1].GetInt("port"))
	}
}

// The wait exists because the joiner asks ~1s before the host reports. What matters is
// that the answer carries the bridged urls once the host catches up.
func TestJoinerIsAnsweredOnceTheHostReportsItsReplaceURL(t *testing.T) {
	writeNatFile(t, "203.0.113.9 49275\n")

	host := &Connection{Settings: testSettings(), PID: 1001}
	host.SetStations(hostBeforeReplaceURL())

	// Not bridgeable yet.
	if _, status := natBridgeStations(host.Stations(), false); status != bridgeNoRVCID {
		t.Fatal("host is not ready; bridge must report the RVCID shortfall")
	}

	// The host reports it a moment later, from its own goroutine.
	go func() {
		time.Sleep(150 * time.Millisecond)
		host.SetStations(hostAfterReplaceURL())
	}()

	deadline := time.Now().Add(hostReplaceURLWait)
	var bridged bool
	for time.Now().Before(deadline) {
		time.Sleep(hostReplaceURLPoll)
		if _, status := natBridgeStations(host.Stations(), false); status == bridgeOK {
			bridged = true

			break
		}
	}

	if !bridged {
		t.Error("the wait must pick up the host's ReplaceURL once it lands")
	}
}

// Stations() is read by OTHER connections' goroutines while the owner replaces them.
// Under -race this fails loudly without the lock; without -race it still catches a torn
// or empty read.
func TestStationsAreSafeToReadWhileTheOwnerReplacesThem(t *testing.T) {
	host := &Connection{Settings: testSettings(), PID: 1001}
	host.SetStations(hostBeforeReplaceURL())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			host.SetStations(hostAfterReplaceURL())
			host.SetStations(hostBeforeReplaceURL())
		}
	}()

	for i := 0; i < 500; i++ {
		for _, u := range host.Stations() {
			if u.Get("address") == "" {
				t.Fatal("read a station with no address: the slice was swapped mid-read")
			}
		}
	}

	<-done
}

// A host that never reports must not hold the joiner forever: the wait is bounded, and
// past it the joiner gets the raw stations rather than nothing at all.
func TestWaitIsBounded(t *testing.T) {
	if hostReplaceURLWait > 3*time.Second {
		t.Errorf("wait of %v is longer than a joiner will sit there", hostReplaceURLWait)
	}
	if hostReplaceURLPoll >= hostReplaceURLWait {
		t.Error("poll interval must be well under the deadline or the wait checks once")
	}
}
