package nex

import (
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
		defer m.mu.Unlock()
		g := m.gatherings[gid]
		if g == nil || !containsPID(g.participants, caller.PID) || caller.Endpoint.FindConnectionByPID(caller.PID) != caller {
			return
		}
		// A player may have left while the notification was waiting.
		for _, pid := range parts {
			if !containsPID(g.participants, pid) {
				return
			}
		}
		m.notifyParticipation(caller, parts, gid, "")
	})
}

func (m *Matchmaking) bridgeSessionStations(urls []*StationURL) ([]*StationURL, bridgeStatus) {
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
