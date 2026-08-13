package nex

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// natbridge: hands a joiner the host's REAL UDP endpoint at GetSessionURLs.
//
// Why this exists. Pia probes the nncs NAT-check responder AFTER it has registered its
// station URL, and what it registers is its WebSocket TCP port — useless for a UDP
// hole-punch. So the URLs a host reports are, for P2P purposes, wrong. The nncs responder
// is the only component that ever observes the client's real external UDP endpoint; it
// writes each one to a file, and this reads it back so the session URLs point somewhere a
// probe can actually reach.
//
// Without it the joiner gets the TCP port, the probe never lands, and the console gives
// up: it stops after MatchMakingExt m=1 and re-registers in a loop until the game shows a
// connection error. That is the exact shape of the MK8 worldwide failure, and it is why
// worldwide broke when MK8 moved to this core — the nex-go stack had this bridge
// (nex-protocols-common-go/match-making/get_session_urls.go), and it was not ported.
//
// The file is written by the nncs responder co-located with the game server
// (NNCS_NAT_FILE). It MUST be local: a responder on another host cannot share it.

// bridgeStatus says whether the bridge ran, and if not, whether waiting could help.
// The distinction matters: GetSessionURLs is on the join path, so a caller must only
// stall for a shortfall that a moment will actually fix.
type bridgeStatus int

const (
	bridgeOK bridgeStatus = iota
	// bridgeNoStations: the host reported no usable public/private pair. Structural —
	// waiting changes nothing.
	bridgeNoStations
	// bridgeNoObservation: nncs never saw this host's UDP endpoint. Nothing here can
	// produce one, and the joiner is better served by an immediate answer.
	bridgeNoObservation
	// bridgeNoRVCID: everything is present except the id the host reports at ReplaceURL,
	// about a second after a joiner typically asks. THIS is the race worth waiting out.
	bridgeNoRVCID
)

// natFilePath is where the co-located nncs responder writes "<public ip> <udp port>" lines.
func natFilePath() string {
	if p := os.Getenv("NNCS_NAT_FILE"); p != "" {
		return p
	}
	return "/data/nat_endpoints.txt"
}

// The file is rewritten whole on every probe, and GetSessionURLs is on the hot path of
// every join. Cache it briefly rather than re-reading per call; 2s is far below how long
// an endpoint stays valid and still picks up a fresh probe within one matchmaking round.
var (
	natCacheMu   sync.Mutex
	natCache     map[string]int
	natCacheRead time.Time
)

const natCacheTTL = 2 * time.Second

// Station relay configuration (opt-in, OFF by default).
//
// A game server that cannot rely on direct P2P (both peers behind firewalled NATs with no
// port forwarding) can stand in for the peers' station endpoint and forward between them.
// Nothing here changes behaviour until a server calls SetStationRelay: relayHost stays
// empty, StationRelay reports disabled, and every existing title keeps the plain natbridge
// path untouched. Introduced for the Luigi's Mansion 3 server.
var (
	relayMu   sync.RWMutex
	relayHost string
	relayPort int
)

// SetStationRelay switches the station relay on (host + port of the relay socket) or off
// (empty host). Off by default.
func SetStationRelay(host string, port int) {
	relayMu.Lock()
	defer relayMu.Unlock()
	relayHost = host
	relayPort = port
}

// StationRelay reports the relay configuration (host, port, enabled).
func StationRelay() (string, int, bool) {
	relayMu.RLock()
	defer relayMu.RUnlock()
	return relayHost, relayPort, relayHost != "" && relayPort > 0
}

// NatPortForIP exposes the external UDP port the nncs responder observed for a public IP —
// the endpoint peers must actually send station traffic to. Exported for game servers that
// shape their own station records off the observed endpoint.
func NatPortForIP(ip string) (int, bool) {
	return natPortForIP(ip)
}

// natPortForIP returns the external UDP port the nncs responder observed for a public IP.
func natPortForIP(ip string) (int, bool) {
	if ip == "" {
		return 0, false
	}

	natCacheMu.Lock()
	defer natCacheMu.Unlock()

	if natCache == nil || time.Since(natCacheRead) > natCacheTTL {
		data, err := os.ReadFile(natFilePath())
		if err != nil {
			// No file: the responder is not co-located, or has not written yet. Report
			// nothing rather than a stale guess — the caller falls back to the raw URLs.
			natCache = map[string]int{}
			natCacheRead = time.Now()

			return 0, false
		}

		m := make(map[string]int)
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			if p, err := strconv.Atoi(f[1]); err == nil && p > 0 && p < 65536 {
				m[f[0]] = p
			}
		}
		natCache = m
		natCacheRead = time.Now()
	}

	p, ok := natCache[ip]

	return p, ok
}

// natBridgeStations rebuilds a host's station URLs into the two-candidate shape a console
// needs to run its NAT probe, with the real UDP port from the nncs observation.
//
// The result is Nintendo's shape, measured from the wire:
//   - a LAN candidate: the host's private address, the real UDP port, and the RVCID the
//     host reported at ReplaceURL. It carries NEITHER "type" NOR "Pa" — those mark a URL
//     as public, and a LAN candidate that looks public is not recognised as one, which is
//     precisely what makes the console fall through to MatchMakingExt m=1 and stall.
//   - a public candidate: the host's public address, the real UDP port, type=11, and
//     Pa=<private address>.
//
// Returns the urls untouched whenever it cannot do better — no NAT observation, no
// private address, no RVCID. A wrong port is a broken match; the raw URLs are at least
// what the previous behaviour handed out.
func natBridgeStations(urls []*StationURL, publicFirst bool) ([]*StationURL, bridgeStatus) {
	local, public := selectStations(urls)
	if local == nil || public == nil {
		// Both candidates are required. A host reporting only one usually means its
		// "local" address did not look private to us — a VPN adapter (Radmin/Hamachi
		// hand out 26.x, ZeroTier 28.x) is reported as the LAN address and reads as
		// public, so selectStations files both urls under public and finds no local.
		natBridgeSkip("no local/public pair", urls)

		return urls, bridgeNoStations
	}

	publicAddr := public.Get("address")
	privateAddr := local.Get("address")
	if publicAddr == "" || privateAddr == "" || publicAddr == privateAddr {
		natBridgeSkip("public and private address are the same or empty", urls)

		return urls, bridgeNoStations
	}

	udpPort, ok := natPortForIP(publicAddr)
	if !ok {
		// The nncs responder never saw this host. Either it has not probed yet, or its
		// probe went to the OTHER responder — the one on the other machine, whose file
		// this server cannot read.
		natBridgeSkip("no nncs observation for "+publicAddr, urls)

		return urls, bridgeNoObservation
	}

	// The RVCID comes from the host's ReplaceURL, sent AFTER its NAT handshake. This is the
	// one shortfall worth waiting on: the host is expected to report it within about a
	// second, and a joiner routinely asks before it does.
	cid := local.GetInt("RVCID")
	if cid == 0 {
		return urls, bridgeNoRVCID
	}

	// La station LAN se construit à partir de la station LOCALE de l'hôte (son ReplaceURL), PAS de
	// la publique : elle porte déjà l'adresse privée, les vrais natf/natm de l'hôte, le RVCID et —
	// surtout — le CID (l'identifiant de connexion de l'hôte dont la Pia du visiteur a besoin pour
	// MONTER la session P2P après le perçage). L'ancien code copiait la station publique et
	// PERDAIT ce CID : le visiteur perçait le NAT mais la session Pia ne se montait jamais →
	// « console ne répond pas » (2618-0502). On n'échange que le port UDP (celui observé par nncs) ;
	// type/Pa sont retirés pour que la console reconnaisse le candidat LOCAL (une URL LAN portant
	// type=public fait sauter le candidat par Pia). Prouvé par la capture nex-go GetSessionURLs :
	// LAN = address=<privée>;port=<udp>;CID=<cid du ReplaceURL>;RVCID.
	lan := local.Copy()
	lan.SetInt("port", udpPort)
	lan.Remove("type")
	lan.Remove("Pa")

	pub := public.Copy()
	pub.SetInt("port", udpPort)
	pub.SetInt("type", int(StationURLFlagBehindNAT|StationURLFlagPublic|stationURLFlagSwitch))
	// Pa mirrors the proven server. MK8/S2 send Pa=<private> (the default); ACNH's Pia
	// instead reads the public candidate's Pa as the endpoint to reach, so it must be the
	// PUBLIC address — the nex-go server ACNH is known to work against sends address==Pa==
	// public (capture acnh nexgo_reference.bin). Pa=<private> there sends the joiner's probe
	// to the host's LAN address and stalls it at "getting ready to depart" (2618-0502).
	pa := privateAddr
	if publicFirst {
		pa = publicAddr
	}
	pub.Set("Pa", pa)

	// Say so on every substitution. Whether this bridge fires is the whole difference
	// between a joinable host and a session that stalls at MatchMakingExt m=1, and it
	// depends on a file written by another process — so it must be visible in the log
	// rather than inferred from players reporting that it works.
	fmt.Printf("[natbridge] %s: registered tcp port %d -> observed udp port %d (lan=%s rvcid=%d, publicFirst=%v)\n",
		publicAddr, public.GetInt("port"), udpPort, privateAddr, cid, publicFirst)

	// Station order. MK8/S2 take [lan, public] (the default). ACNH's Pia treats the FIRST
	// station as the primary P2P candidate and never falls through to the second, so a
	// remote joiner handed [lan, ...] probes the unreachable 192.168.x address and fails;
	// the nex-go server ACNH works against sends [public, lan]. Reproduce that here.
	if publicFirst {
		return []*StationURL{pub, lan}, bridgeOK
	}
	return []*StationURL{lan, pub}, bridgeOK
}

// natBridgeSkip records why a host's stations were handed over unbridged.
//
// The bridge firing is the difference between a joinable host and a session that stalls,
// and it depends on a file written by another process and on what the console chose to
// report. When it does not fire, the reason is the only thing worth knowing — logging only
// the successes leaves "1 substitution for 6 joins" with no way to explain the other five.
func natBridgeSkip(why string, urls []*StationURL) {
	rendered := make([]string, 0, len(urls))
	for _, u := range urls {
		rendered = append(rendered, u.String())
	}

	fmt.Printf("[natbridge] SKIP (%s) — handing over raw stations: %s\n",
		why, strings.Join(rendered, " | "))
}
