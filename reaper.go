package nex

import (
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// Dead connection eviction.
//
// PRUDP has no reliable close: a console that crashes, loses its network, or
// whose emulator is killed never says goodbye. Without eviction, its connection
// stayed registered FOREVER. Consequences measured in production on 2026-07-20:
//   - connections "active" for 24 h, 45 h, 47 h without a single packet;
//   - on Mario Kart, ALL 9 listed players were dead connections;
//   - the "one place at a time" guard saw the stale player and denied access:
//     members found themselves permanently locked out and had to re-create accounts;
//   - online player counters were wrong (Smash showed 14 for 7 real players).
//
// So we evict any connection with no traffic since reaperMaxIdle. The threshold
// is generous: a match generates continuous traffic, and a console idling in a
// menu still sends PINGs. Adjustable via NEXTENDO_REAP_IDLE_SECONDS (0 = disabled).

const (
	defaultReapIdleSeconds = 600 // 10 min without a single packet = dead connection
	defaultReapEverySecond = 60  // fréquence de passage du reaper
)

func envSeconds(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(def) * time.Second
}

// ReapIdleTimeout returns the duration of no traffic after which a connection is evicted.
func ReapIdleTimeout() time.Duration {
	return envSeconds("NEXTENDO_REAP_IDLE_SECONDS", defaultReapIdleSeconds)
}

// snapshotConns returns a copy of the connection list. We NEVER call close()
// while holding connMu: close() calls unregisterConnection, which takes the same lock.
func (e *Endpoint) snapshotConns() []*Connection {
	e.connMu.Lock()
	out := make([]*Connection, 0, len(e.connections))
	for _, c := range e.connections {
		out = append(out, c)
	}
	e.connMu.Unlock()
	return out
}

// ReapIdle closes connections with no traffic since maxIdle and returns the count evicted.
// maxIdle <= 0 disables eviction (no connections are touched).
func (e *Endpoint) ReapIdle(maxIdle time.Duration) int {
	if maxIdle <= 0 {
		return 0
	}
	n := 0
	for _, c := range e.snapshotConns() {
		// lastSeen at zero = handshake in progress, never evicted.
		if idle := c.IdleFor(); idle > 0 && idle >= maxIdle {
			log.Printf("[reaper] dead connection evicted: pid=%d rvcid=%d addr=%s (no traffic for %s)",
				c.PID, c.ID, c.RemoteAddr, idle.Round(time.Second))
			c.Close()
			n++
		}
	}
	return n
}

// KickPID closes all connections for a PID and returns the count. Used to
// manually free a stuck player ("this account is already playing elsewhere")
// without restarting the server, which would disconnect everyone.
func (e *Endpoint) KickPID(pid uint64) int {
	if pid == 0 {
		return 0
	}
	n := 0
	for _, c := range e.snapshotConns() {
		if c.PID == pid {
			log.Printf("[kick] pid=%d rvcid=%d addr=%s disconnected (admin request)", c.PID, c.ID, c.RemoteAddr)
			c.Close()
			n++
		}
	}
	return n
}

// KickConnection closes ONE connection by rvcid. Returns true if it existed.
func (e *Endpoint) KickConnection(id uint32) bool {
	for _, c := range e.snapshotConns() {
		if c.ID == id {
			log.Printf("[kick] rvcid=%d pid=%d addr=%s disconnected (admin request)", c.ID, c.PID, c.RemoteAddr)
			c.Close()
			return true
		}
	}
	return false
}

// StartReaper launches periodic eviction in a background goroutine. Idempotent: safe to call
// from each game server without starting multiple loops.
func (e *Endpoint) StartReaper() {
	maxIdle := ReapIdleTimeout()
	if maxIdle <= 0 {
		log.Printf("[reaper] eviction DISABLED (NEXTENDO_REAP_IDLE_SECONDS=0)")
		return
	}
	every := envSeconds("NEXTENDO_REAP_EVERY_SECONDS", defaultReapEverySecond)
	e.reaperOnce.Do(func() {
		log.Printf("[reaper] active: evicting connections idle over %s (check every %s)", maxIdle, every)
		go func() {
			for {
				time.Sleep(every)
				if n := e.ReapIdle(maxIdle); n > 0 {
					log.Printf("[reaper] %d dead connection(s) evicted", n)
				}
			}
		}()
	})
}
