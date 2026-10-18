package nex

// gws (ParallelEnabled) traite jusqu'a 8 messages a la fois pour une meme socket : Feed est
// donc appele en parallele sur la meme Connection. Ce test reproduit ce cas ; lance avec
// -race, il signalait avant le correctif des acces concurrents a recvBuf, fragBuf, fragRejet,
// journalAvantConnect et state. Sans -race, il verifie que chaque requete est servie une fois.

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestFeedEnParalleleSurUneSocket(t *testing.T) {
	s := testSettings()
	for _, secure := range []bool{false, true} {
		ep := NewEndpoint(s)
		if secure {
			ep.SetSecureAccount("mot-de-passe-de-test", 2)
		}
		ep.MaxRMCSize = 1 << 20
		var appels atomic.Int32
		ep.Register(0x6D, func(c *Connection, req *RMCMessage) *RMCMessage {
			appels.Add(1)

			return NewRMCSuccess(c.Settings, req.Protocol, req.Method, req.CallID, []byte{1})
		})
		var mu sync.Mutex
		conn := NewConnection(ep, "192.0.2.41:1241", func([]byte) { mu.Lock(); mu.Unlock() })
		rmc := NewRMCRequest(s, 0x6D, 1, 7, []byte{1, 2, 3}).Encode()
		const goroutines, parG = 8, 100
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < parG; i++ {
					conn.Feed(EncodePacket(dataPacket(rmc, uint16(g*parG+i+1))))
				}
			}(g)
		}
		// Fermeture concurrente (OnClose du transport) pendant les DATA.
		wg.Add(1)
		go func() { defer wg.Done(); conn.Close() }()
		wg.Wait()
		want := int32(goroutines * parG)
		if secure {
			want = 0 // jamais connectee : aucune DATA servie
		}
		if n := appels.Load(); n != want {
			t.Fatalf("secure=%v : %d requete(s) servie(s), attendu %d", secure, n, want)
		}
	}
}
