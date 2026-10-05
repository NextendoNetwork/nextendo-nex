package nex

import (
	"fmt"
	"net"
	"time"
)

func (m *Matchmaking) notifyParticipationWithDelay(caller *Connection, participants []uint64, gid uint32) {
	if m.ParticipationNotificationDelay <= 0 {
		m.notifyParticipation(caller, participants, gid, "")
		return
	}
	parts := append([]uint64(nil), participants...)
	time.AfterFunc(m.ParticipationNotificationDelay, func() {
		m.mu.Lock()
		g := m.gatherings[gid]
		if g == nil || !containsPID(g.participants, caller.PID) || caller.Endpoint.FindConnectionByPID(caller.PID) != caller {
			m.mu.Unlock()
			return
		}
		// A player may have left while the notification was waiting. Keep the
		// original snapshot order, but do not let one departure suppress the
		// notification for everyone who is still in the gathering.
		remaining := make([]uint64, 0, len(parts))
		for _, pid := range parts {
			if containsPID(g.participants, pid) {
				remaining = append(remaining, pid)
			}
		}
		m.mu.Unlock()

		// Match the existing matchmaking call sites: socket writes and callbacks
		// must run outside the matchmaking critical section.
		m.notifyParticipation(caller, remaining, gid, "")
	})
}

func (m *Matchmaking) bridgeSessionStations(urls []*StationURL) ([]*StationURL, bridgeStatus) {
	if m.PreservePiaStationIdentity {
		local, public := selectStations(urls)
		if local == nil || public == nil {
			return urls, bridgeNoStations
		}
		if local.GetInt("CID") == 0 || public.GetInt("CID") != local.GetInt("CID") {
			return urls, bridgeNoRVCID
		}
		if m.PublicStationFirst {
			return []*StationURL{public, local}, bridgeOK
		}
		return []*StationURL{local, public}, bridgeOK
	}
	if !m.LocalLoopbackStations || m.PublicStationFirst {
		return natBridgeStations(urls, m.PublicStationFirst)
	}
	var local, public *StationURL
	for _, u := range urls {
		ip := net.ParseIP(u.Get("address"))
		if ip == nil {
			continue
		}
		if ip.IsLoopback() && uint8(u.GetInt("type"))&StationURLFlagPublic != 0 {
			public = u
		} else if !ip.IsLoopback() && isPrivateIP(u.Get("address")) {
			local = u
		}
	}
	if local == nil || public == nil {
		return natBridgeStations(urls, m.PublicStationFirst)
	}
	// ReplaceURL supplies the per-client UDP port; an IP-only NNCS cache cannot
	// distinguish two emulator instances sharing 127.0.0.1.
	if local.GetInt("CID") == 0 || local.GetInt("RVCID") == 0 || local.GetInt("port") <= 1 || local.GetInt("port") > 65535 {
		return urls, bridgeNoRVCID
	}
	if public.GetInt("port") <= 0 || public.GetInt("port") > 65535 {
		return urls, bridgeNoStations
	}
	lan, pub := local.Copy(), public.Copy()
	lan.Remove("type")
	lan.Remove("Pa")
	// Keep the public port: the host still sends it in its PIA location.
	pub.SetInt("type", int(StationURLFlagBehindNAT|StationURLFlagPublic|stationURLFlagSwitch))
	pub.Set("Pa", lan.Get("address"))
	return []*StationURL{lan, pub}, bridgeOK
}

// bridgeSessionStationsForPair keeps the host's own UDP port when the two
// consoles share a public IP. The NNCS observation file has only one port per
// IP; after the joiner probes, that entry can be the joiner's port. Replacing
// the host's LAN port with it makes the joiner probe itself.
func (m *Matchmaking) bridgeSessionStationsForPair(joiner, host *Connection) ([]*StationURL, bridgeStatus) {
	urls := host.Stations()
	if !behindSameNAT(joiner, host) || m.PreservePiaStationIdentity {
		return m.bridgeSessionStations(urls)
	}
	local, public := selectStations(urls)
	if local == nil || public == nil {
		return urls, bridgeNoStations
	}
	port := local.GetInt("port")
	if local.GetInt("RVCID") == 0 || port <= 1 || port > 65535 {
		return urls, bridgeNoRVCID
	}
	lan := local.Copy()
	lan.Remove("type")
	lan.Remove("Pa")
	pub := public.Copy()
	pub.SetInt("port", port)
	pub.SetInt("type", int(StationURLFlagBehindNAT|StationURLFlagPublic|stationURLFlagSwitch))
	pub.Set("Pa", lan.Get("address"))
	fmt.Printf("[natbridge] shared public IP: host pid=%d LAN UDP port=%d (NNCS IP cache ignored)\n", host.PID, port)
	return []*StationURL{lan, pub}, bridgeOK
}
