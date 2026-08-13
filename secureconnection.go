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
	// (BehindNAT|Public). Wire capture with the client held constant: the proven S2 server
	// answers type=3, the proven SSBU one type=11.
	PublicStationType uint8
	// SetPa adds Pa=<address> to the public station. Pia 5.19 needs it to build the NAT
	// probe; the proven S2 server sends NO Pa at all.
	SetPa bool

	// PublicHost / P2PAnchorPort / P2PRelay are consumed by servers that stand in for a
	// host's station endpoint (a co-located host, or peers that can never reach each
	// other directly). They are accepted here so such a server can configure itself; the
	// shared Register path deliberately does NOT act on them, so every existing title
	// keeps the plain behaviour.
	PublicHost    string
	P2PAnchorPort int
	P2PRelay      bool
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
			return handleReplaceURL(conn, req)
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
		// console sends (capture ssbu_wsframes_2319: address=88.161.250.17, Pa=10.7.2.108).
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
	fmt.Printf("[Register] pid=%d -> local=%s\n           public=%s\n", conn.PID, local.String(), public.String())

	out := NewStreamOut(s)
	out.U32(ResultCoreUnknown)  // retval (success)
	out.U32(conn.ID)            // pidConnectionID (RVCID)
	out.String(public.String()) // urlPublic
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
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	_ = in.StationURLValue() // target (the url being replaced) — unused; we dedupe by address
	newStation := in.StationURLValue()
	if newStation == nil {
		return NewRMCSuccess(s, ProtocolSecureConnection, req.Method, req.CallID, nil)
	}
	newStation.SetInt("PID", int(conn.PID))
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

	fmt.Printf("[ReplaceURL] pid=%d -> new=%s (now %d station(s))\n", conn.PID, newStation.String(), len(rebuilt))
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
