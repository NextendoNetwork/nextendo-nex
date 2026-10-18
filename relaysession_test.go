package nex

import (
	"strconv"
	"testing"
)

// Le relais par paire ne repointe l InitiateProbe que pour la paire (arrivant, hote) dont
// les URL de session viennent d etre repointees (relayedFor) : un autre membre du salon
// (2 contre 2) a recu l adresse de l arrivant par l hote, en direct.
func TestRelaisInitiateProbeSeulementPaireDeSession(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	var bSent [][]byte
	connB := NewConnection(ep, "198.51.100.2:1", func(b []byte) { bSent = append(bSent, append([]byte(nil), b...)) })
	connB.PID = 1800900002
	ep.registerConnection(connB)
	connA := NewConnection(ep, "198.51.100.1:1", func([]byte) {})
	connA.PID = 1800900001
	ep.registerConnection(connA)

	SetPairRelay("203.0.113.9", 31500, 50)
	SetRelayVolontaires(nil, []uint64{connA.PID, connB.PID}, false)
	t.Cleanup(func() {
		SetPairRelay("", 0, 0)
		SetRelayVolontaires(nil, nil, false)
		closeAllPairSockets()
	})

	sonde := func() string {
		t.Helper()
		bSent = nil
		cible := NewStationURL("prudp")
		cible.Set("address", "198.51.100.2")
		cible.SetInt("RVCID", int(connB.ID))
		body := NewStreamOut(s)
		WriteList(body, []string{cible.String()}, func(o *StreamOut, u string) { o.String(u) })
		body.String("prudp:/address=198.51.100.1;port=1234;natm=1;RVCID=" + strconv.Itoa(int(connA.ID)))
		if r := NATTraversalHandler()(connA, NewRMCRequest(s, ProtocolNATTraversal, MethodRequestProbeInitiationExt, 5, body.Bytes())); r.IsError {
			t.Fatalf("RequestProbeInitiationExt : %+v", r)
		}
		if len(bSent) != 1 {
			t.Fatalf("B a recu %d paquet(s)", len(bSent))
		}
		pushed, err := ParseRMC(s, decodeOne(t, bSent[0]).Payload)
		if err != nil {
			t.Fatal(err)
		}
		return ParseStationURL(NewStreamIn(pushed.Body, s).String()).Get("address")
	}

	if a := sonde(); a != "198.51.100.1" {
		t.Fatalf("paire sans URL de session relayee : InitiateProbe repointe vers %q", a)
	}
	NoterSessionRelayee(connA.PID, connB.PID)
	if a := sonde(); a != "203.0.113.9" {
		t.Fatalf("paire (arrivant, hote) relayee : InitiateProbe vers %q au lieu du relais", a)
	}
}

// classePia reproduit le choix du code d erreur du jeu.
func TestClassePia(t *testing.T) {
	ep := NewEndpoint(testSettings())
	avec := func(addr string, natm int) *Connection {
		c := NewConnection(ep, addr, func([]byte) {})
		u := NewStationURL("prudp")
		u.Set("address", "10.0.0.1")
		if natm != 0 {
			u.SetInt("natm", natm)
		}
		c.SetStations([]*StationURL{u})
		return c
	}
	cas := []struct {
		a, b   *Connection
		classe string
	}{
		{avec("198.51.100.1:1", 1), avec("198.51.100.2:1", 1), "0513"},
		{avec("198.51.100.1:1", 1), avec("198.51.100.2:1", 2), "0515"},
		{avec("198.51.100.1:1", 2), avec("198.51.100.2:1", 1), "0516"},
		{avec("198.51.100.1:1", 2), avec("198.51.100.2:1", 2), "0514"},
		{avec("198.51.100.1:1", 1), avec("198.51.100.1:2", 1), "0583-0586"},
		{avec("198.51.100.1:1", 0), avec("198.51.100.2:1", 1), "?"},
	}
	for i, c := range cas {
		if got := classePia(c.a, c.b); got != c.classe {
			t.Fatalf("cas %d : %s, attendu %s", i, got, c.classe)
		}
	}
}
