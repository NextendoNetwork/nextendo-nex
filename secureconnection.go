package nex

import (
	"fmt"
	"net"
)

// SecureConnection protocol (the client registers its station URLs here).
const (
	ProtocolSecureConnection uint16 = 0x0B
	MethodRegister           uint32 = 0x1
	MethodReplaceURL         uint32 = 0x7
	MethodRegisterEx         uint32 = 0x4
)

// StationURL type flags.
const (
	StationURLFlagBehindNAT uint8 = 0x01
	StationURLFlagPublic    uint8 = 0x02
	// stationURLFlagSwitch is the extra bit the Switch's Pia sets on the public
	// station, giving type = 0x0B (BehindNAT | Public | 0x08).
	stationURLFlagSwitch uint8 = 0x08
)

// SecureConnectionConfig tunes what Register advertises on the derived public station.
// The right values are GAME-SPECIFIC — they follow the title's Pia version, not our
// preference — so each game passes what its proven server was measured to send.
type SecureConnectionConfig struct {
	// PublicStationType is the `type` param on the public station. Switch Pia 5.19 (SSBU)
	// wants 0x0B (BehindNAT|Public|Switch); older Pia (Splatoon 2) wants the Wii U-era 0x03
	// (BehindNAT|Public). measurement with the client held constant: the proven S2 server
	// answers type=3, the proven SSBU one type=11.
	PublicStationType uint8
	// SetPa adds Pa=<address> to the public station. Pia 5.19 needs it to build the NAT
	// probe; the proven S2 server sends NO Pa at all.
	SetPa bool
	// PublicHost is the server's own public IP (NEXTENDO_HOST). A host co-located with
	// this server hairpins through its own router, so its probes arrive from a private
	// address and its game reports a VPN-looking LAN address (Radmin/Hamachi/ZeroTier)
	// as both local and public — a pair the natbridge cannot split, and a public
	// candidate no peer can reach. When set, Register/ReplaceURL repoint such hosts at
	// this address. Empty = no correction.
	PublicHost string
	// P2PAnchorPort is a fixed UDP port that stays bound, FORWARDED and RESPONDING for
	// P2P probes (the nncs responder also serves it). The game's resolve job probes its
	// station URLs with the same 16-byte probe format it uses on nncs, and S2 accepts
	// the resolve only when every reply reports the SAME client source port (it never
	// checks which IP the reply came from). A co-located host's own station — its
	// VPN/LAN address — is unreachable or unresponsive, so the resolve never sees a
	// consistent port and the host dies with 2618-201 after a few retry bursts. The
	// anchor points the station at a port our responder answers, so the resolve always
	// completes. 0 = disable (pass the host's own stations through, stock behaviour).
	P2PAnchorPort int
	// P2PRelay makes the server stand in for EVERY host's station — not just a
	// co-located one. Register stores the public station at PublicHost:P2PAnchorPort
	// (the server's relay socket) for all hosts, so every client's resolve probes,
	// punch attempts and station traffic land on the relay port, which the game
	// server forwards between peers. This is what connects two players who are both
	// behind firewalled NATs with no port forwarding: neither can reach the other's
	// real endpoint, but both can reach (and keep a NAT mapping toward) the server.
	// Requires PublicHost and P2PAnchorPort to be set. Disabled by default.
	P2PRelay bool
}

// SwitchPia519Config is what SSBU (Pia 5.19) needs: type=0x0B plus Pa.
func SwitchPia519Config() SecureConnectionConfig {
	return SecureConnectionConfig{
		PublicStationType: StationURLFlagBehindNAT | StationURLFlagPublic | stationURLFlagSwitch,
		SetPa:             true,
	}
}

// LegacyPiaConfig is what Splatoon 2's proven server sends: type=0x03, no Pa.
func LegacyPiaConfig() SecureConnectionConfig {
	return SecureConnectionConfig{
		PublicStationType: StationURLFlagBehindNAT | StationURLFlagPublic,
		SetPa:             false,
	}
}

// SecureConnectionHandler processes Register/RegisterEx: the client reports its
// station URLs; we assign it a connection id and pick its local (LAN) and public
// stations for the P2P NAT bridge. It defaults to the Pia 5.19 shape; use
// SecureConnectionHandlerWithConfig for a title that needs the older one.
func SecureConnectionHandler() RMCHandler {
	return SecureConnectionHandlerWithConfig(SwitchPia519Config())
}

// SecureConnectionHandlerWithConfig is SecureConnectionHandler with an explicit
// per-title station shape.
func SecureConnectionHandlerWithConfig(cfg SecureConnectionConfig) RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodRegister, MethodRegisterEx:
			return handleRegister(conn, req, cfg)
		case MethodReplaceURL:
			return handleReplaceURLWithConfig(conn, req, cfg)
		default:
			return notImplemented(conn, ProtocolSecureConnection, req)
		}
	}
}

func handleRegister(conn *Connection, req *RMCMessage, cfg SecureConnectionConfig) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	urls := ReadList(in, func(i *StreamIn) *StationURL { return i.StationURLValue() })

	local, public := selectStations(urls)
	if local == nil {
		local = NewStationURL("prudp")
	}
	// The Splatoon 2 client reports ONE station url — its LAN address — and this stack
	// serves it for BOTH roles when that address does not look private (a VPN adapter):
	// selectStations files the url under public, then the fallback picks the same
	// pointer as local. A local/public pair that is the same OBJECT cannot be split by
	// the natbridge (the addresses are identical), so Register must take the derived
	// path: keep the url as local and synthesize a SEPARATE public copy. Without this
	// the host's own NAT-keep sees a public station that matches nothing.
	if local != nil && public != nil && (local == public || local.Get("address") == public.Get("address")) {
		public = nil
	}
	// derivedPublic: the client reported no public station, so we synthesised one from
	// the observed endpoint. It changes what Pa must be — see the Pa comment below.
	derivedPublic := public == nil
	if public == nil {
		// No public station reported (the emulator/console reports only LAN URLs):
		// derive the public one by COPYING the local station so it keeps every param
		// the console sent (sid, Pl, Tpt, natf, natm, pmp, upnp), then point it at the
		// observed public endpoint. A bare station (missing those params) makes Pia
		// fail to recognise its own endpoint and give up with 2618-562 SessionKeepFailed.
		public = local.Copy()
		if host, port, err := net.SplitHostPort(conn.RemoteAddr); err == nil {
			public.Set("address", host)
			public.Set("port", port)
		}
	}

	local.SetInt("PID", int(conn.PID))
	public.SetInt("PID", int(conn.PID))
	local.SetInt("RVCID", int(conn.ID))
	public.SetInt("RVCID", int(conn.ID))

	// The public station's `type` (and whether Pa is present at all) follows the title's Pia
	// version, not our preference — see SecureConnectionConfig. Pia 5.19 (SSBU) needs
	// type=0x0B + Pa to build the NAT probe; Splatoon 2's proven server sends type=0x03 and
	// NO Pa. Sending SSBU's shape to S2 is a real divergence from the server it is known to
	// work against.
	public.SetInt("type", int(cfg.PublicStationType))
	if cfg.SetPa {
		// Pa mirrors the proven server: when the client reported no public station of its own
		// (it only sent LAN urls) that server's public station IS its local station — the same
		// pointer — so reading the "local" address back after repointing it returns the PUBLIC
		// one, and Pa ends up being the public address. We reproduce the resulting value, not
		// the aliasing, so `local` stays a real LAN station for the P2P bridge. When the client
		// DOES report its own public station, Pa is the LAN address, which is what a real
		// console sends (measured a measurement: address=203.0.113.20, Pa=10.0.0.108).
		// NOTE: both Pa variants are known to work on SSBU — the real console sends Pa=<LAN>
		// and the proven server sends Pa=<public> — so the VALUE is not load-bearing there.
		pa := local.Get("address")
		if derivedPublic {
			pa = public.Get("address")
		}
		if pa != "" {
			public.Set("Pa", pa)
		}
	}

	conn.SetStations([]*StationURL{local, public})
	fixStationEndpoint(conn, local, public, cfg)

	if cfg.P2PRelay && cfg.PublicHost != "" {
		// Relay mode: the server stands in for this host's station, so point the
		// STORED public station at the relay socket. Everything downstream then
		// inherits the relay address: the natbridge hands out relay URLs at
		// GetSessionURLs, and the 0x79 station records (via Stations()) carry them
		// too — so ALL of a peer's station traffic lands on the relay port.
		if cfg.P2PAnchorPort > 0 {
			public.SetInt("port", cfg.P2PAnchorPort)
		}
		public.Set("address", cfg.PublicHost)
	}

	// The station Pia's resolve job probes (the response) and the station peers get
	// (stored, via the natbridge) must differ for a host co-located with this server.
	// Its probes hairpin through its own router, so the observed endpoint is the
	// router's LAN address — useless as a station for the host itself (192.168.1.1 is
	// the router, nothing listens there) AND for peers. The response must point at an
	// address the host can actually probe-and-get-a-reply-from, which for a co-located
	// host is the server's own public IP (hairpin delivers it back to this LAN).
	respPublic := public.Copy()
	if host, _, err := net.SplitHostPort(conn.RemoteAddr); err == nil {
		switch {
		case cfg.P2PRelay && cfg.PublicHost != "":
			// Relay mode: respPublic is already a copy of the relay-pointed public
			// station — the client's resolve job probes the relay and gets answered.
		case isPrivateIP(host) && cfg.PublicHost != "":
			respPublic.Set("address", cfg.PublicHost)
			if cfg.P2PAnchorPort > 0 {
				respPublic.SetInt("port", cfg.P2PAnchorPort)
			}
		default:
			respPublic.Set("address", host)
			if p, ok := natPortForIP(host); ok {
				respPublic.SetInt("port", p)
			}
		}
	}
	fmt.Printf("[%s] [Register] pid=%d -> local=%s\n           stored=%s\n           resp=%s\n",
		ts(), conn.PID, local.String(), public.String(), respPublic.String())

	out := NewStreamOut(s)
	out.U32(ResultCoreUnknown)      // retval (success)
	out.U32(conn.ID)                // pidConnectionID (RVCID)
	out.String(respPublic.String()) // urlPublic
	return NewRMCSuccess(s, ProtocolSecureConnection, req.Method, req.CallID, out.Bytes())
}

// handleReplaceURL answers ReplaceURL (0x0B/0x7). After the NAT handshake the
// console re-reports its updated LAN station URL — now carrying the connection id
// the joiner needs to build its NAT probe. We MUST store it: dropping it (the old
// NotImplemented) loses the CID-bearing url so the joiner's probe never completes
// ("communication error" on a friend-join). Request = (target StationURL, url
// StationURL); the response is an empty success. We rebuild the station set deduped
// by ADDRESS with the new url winning for its address (keeps the fresh LAN url,
// removes stale duplicates), matching the proven server.
func handleReplaceURL(conn *Connection, req *RMCMessage) *RMCMessage {
	return handleReplaceURLWithConfig(conn, req, SecureConnectionConfig{})
}

func handleReplaceURLWithConfig(conn *Connection, req *RMCMessage, cfg SecureConnectionConfig) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	_ = in.StationURLValue() // target (the url being replaced) — unused; we dedupe by address
	newStation := in.StationURLValue()
	if newStation == nil {
		return NewRMCSuccess(s, ProtocolSecureConnection, req.Method, req.CallID, nil)
	}
	newStation.SetInt("PID", int(conn.PID))
	// The re-reported LAN url carries the same wrong VPN-looking address as Register did;
	// repoint it so the dedupe keeps one local station the natbridge can pair with public.
	fixStationAddressForEndpoint(conn, newStation, cfg)
	// A REMOTE client re-reports its post-NAT LAN url (private address, real UDP port,
	// RVCID) here. Store it pointing at the endpoint we actually observe — the private
	// address is the one peers could never reach, and the port it carries is the one the
	// NAT handshake confirmed. Register already does this for the public station; without
	// it the host's ReplaceURL (192.168.0.20:58108 style) leaks into 0x79 station records
	// and the joiner's direct mesh connection dies before it starts.
	// In relay mode the observed IP is NOT what peers must reach (the server's relay
	// socket is), so the LAN url stays as reported — it is the natbridge's local
	// candidate and the source of the Pa value either way.
	if !cfg.P2PRelay {
		if host, _, err := net.SplitHostPort(conn.RemoteAddr); err == nil && !isPrivateIP(host) {
			if a := newStation.Get("address"); a != host {
				newStation.Set("address", host)
			}
		}
	}
	newAddr := newStation.Get("address")

	byAddr := map[string]*StationURL{}
	var order []string
	for _, st := range conn.Stations() {
		a := st.Get("address")
		if _, seen := byAddr[a]; !seen {
			order = append(order, a)
		}
		byAddr[a] = st
	}
	if _, seen := byAddr[newAddr]; !seen {
		order = append(order, newAddr)
	}
	byAddr[newAddr] = newStation

	rebuilt := make([]*StationURL, 0, len(order))
	for _, a := range order {
		rebuilt = append(rebuilt, byAddr[a])
	}
	conn.SetStations(rebuilt)

	fmt.Printf("[%s] [ReplaceURL] pid=%d -> new=%s (now %d station(s))\n", ts(), conn.PID, newStation.String(), len(rebuilt))
	return NewRMCSuccess(s, ProtocolSecureConnection, req.Method, req.CallID, nil)
}

// selectStations picks the local (private-IP) and public (public-IP) stations
// by ADDRESS. The Switch commonly reports both its LAN and public URLs without
// the Public flag set, so selecting by flag would mislabel them.
func selectStations(urls []*StationURL) (local, public *StationURL) {
	for _, u := range urls {
		addr := u.Get("address")
		if addr == "" {
			continue
		}
		if isPrivateIP(addr) {
			if local == nil {
				local = u
			}
		} else if public == nil {
			public = u
		}
	}
	if local == nil {
		for _, u := range urls {
			if uint8(u.GetInt("type"))&StationURLFlagPublic == 0 {
				local = u
				break
			}
		}
	}
	if local == nil && len(urls) > 0 {
		local = urls[0]
	}
	return local, public
}

// isPrivateIP reports whether addr is an RFC1918 / CGNAT (LAN) address.
// cgnatRange is RFC6598 carrier-grade NAT space. A console behind CGNAT reports one of
// these as its "LAN" address, and it is no more reachable from another player than an
// RFC1918 one, so it belongs on the local side of the split.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPrivateIP(addr string) bool {
	// Parsed and range-checked rather than prefix-matched. "172." matched the whole /8,
	// so a public 172.217.x (Google) read as private and would be picked as the LAN
	// candidate over the real one; and "100.64." covered only a quarter of the CGNAT /10,
	// so 100.65-127 read as public. Both corrupt the local/public split the natbridge
	// depends on.
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}

	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || cgnatRange.Contains(ip)
}

// fixStationEndpoint repoints a host's registered stations so its resolve job and its
// peers can actually probe them. Run AFTER SetStations so the [Register] log shows the
// corrected shape.
//
// A host co-located with this server probes through its own router, so every probe
// arrives from a PRIVATE address (hairpin). Its game reports the address of the
// interface the emulator chose — often a VPN adapter (Radmin 26.x, Hamachi 25.x,
// ZeroTier 172.28.x) — as BOTH its local and public station. That shape breaks the
// natbridge two ways: the local/public pair is identical (selectStations files both
// under public), and the address is one no peer can route to. When cfg.PublicHost is
// set, we repoint public at the server's own public IP and local at the observed
// hairpin source, restoring the distinct private/public pair the natbridge needs.
func fixStationEndpoint(conn *Connection, local, public *StationURL, cfg SecureConnectionConfig) {
	obsHost, _, err := net.SplitHostPort(conn.RemoteAddr)
	if err != nil || obsHost == "" {
		return
	}

	if isPrivateIP(obsHost) {
		if cfg.PublicHost == "" {
			return
		}
		public.Set("address", cfg.PublicHost)
		if cfg.P2PAnchorPort > 0 {
			public.SetInt("port", cfg.P2PAnchorPort)
		}
		if !isPrivateIP(local.Get("address")) {
			local.Set("address", obsHost)
		}
		return
	}

	// Remote client: the public station must point at the endpoint we observed. A
	// client that reported no public station already gets this in handleRegister's
	// derivedPublic path; this also fixes clients that reported a VPN-looking
	// address as their public one.
	if pa := public.Get("address"); pa != obsHost {
		public.Set("address", obsHost)
	}
}

// fixStationAddressForEndpoint is fixStationEndpoint for the single station a host
// re-reports at ReplaceURL (its post-NAT LAN url, carrying the RVCID peers need). The
// same VPN-looking address comes back here; repoint it at the hairpin source so the
// dedupe-by-address in handleReplaceURL keeps one local station the natbridge can pair.
func fixStationAddressForEndpoint(conn *Connection, st *StationURL, cfg SecureConnectionConfig) {
	obsHost, _, err := net.SplitHostPort(conn.RemoteAddr)
	if err != nil || obsHost == "" || !isPrivateIP(obsHost) || cfg.PublicHost == "" {
		return
	}
	if !isPrivateIP(st.Get("address")) {
		st.Set("address", obsHost)
	}
}
