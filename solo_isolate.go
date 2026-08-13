package nex

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// [Nextendo] Solo-isolation.
//
// PIDs listed in the NEX_SOLO_PIDS env var (comma-separated) always get their OWN
// gathering in autoMatchmake instead of joining an existing one. This gives a private
// test account a reliable SOLO online lobby on a SHARED game server, sidestepping the
// P2P/NAT hole-punch that fails (2618-0521 "a console isn't responding") when it gets
// matched with real, unreachable consoles.
//
// The env is parsed once. Set it ONLY on the game server that should isolate those PIDs
// (e.g. mk8cs) so other games/servers are unaffected.
var (
	soloPIDsOnce sync.Once
	soloPIDsSet  map[uint64]bool
)

func isSoloPID(pid uint64) bool {
	soloPIDsOnce.Do(func() {
		soloPIDsSet = make(map[uint64]bool)
		for _, tok := range strings.Split(os.Getenv("NEX_SOLO_PIDS"), ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if v, err := strconv.ParseUint(tok, 10, 64); err == nil {
				soloPIDsSet[v] = true
			}
		}
	})
	return soloPIDsSet[pid]
}
