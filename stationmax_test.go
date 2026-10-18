package nex

// Station poussee par RequestProbeInitiationExt : plus de 1 Kio, aucun InitiateProbe (avant :
// recopiee vers chaque cible, gardee 2,5 s par cible pas prete et journalisee en entier pour
// chaque cible, jusqu'a la borne MaxRMCSize de 1 Mio). Temoin : une station de prod, servie.
// N'emploie que des symboles presents avant le correctif : pose avant, il echoue.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStationTropLongueNonPoussee(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	appelant, _ := connCapte(ep, 1800900011, "198.51.100.41:50000")
	cible, ccible := connCapte(ep, 1800900012, "198.51.100.42:50000")
	nat := NATTraversalHandler()
	// 168 octets : la longueur des stations de prod (172 au plus le 16/10).
	court := fmt.Sprintf("prudp:/address=198.51.100.41;port=50000;CID=2385962744;PID=1800900011;RVCID=%d;natf=17;natm=1;probeinit=0;sid=30;type=11;pl=2", appelant.ID)
	for _, cas := range []struct {
		nom     string
		station string
		pousse  bool
	}{
		{"station de 2 Kio", court + ";x=" + strings.Repeat("a", 2048), false},
		{"station de 1025 octets", court + ";x=" + strings.Repeat("a", 1025-len(court)-3), false},
		{"station de 1024 octets (limite)", court + ";x=" + strings.Repeat("a", 1024-len(court)-3), true},
		{"temoin : station de prod", court, true},
	} {
		avant := probesRecus(t, s, ccible)
		if resp := nat(appelant, probeReq(s, 1, cible, cas.station)); resp == nil || resp.IsError {
			t.Fatalf("%s : reponse %+v, attendu l'ACK habituel", cas.nom, resp)
		}
		time.Sleep(20 * time.Millisecond)
		apres := probesRecus(t, s, ccible)
		if got := apres > avant; got != cas.pousse {
			t.Fatalf("%s (%d octets) : InitiateProbe pousse=%v, attendu %v", cas.nom, len(cas.station), got, cas.pousse)
		}
	}
}

// probesRecus compte les InitiateProbe recus, fragments reassembles (une station de 2 Kio
// part en deux fragments).
func probesRecus(t *testing.T, s *Settings, c *capte) int {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	var buf []byte
	for _, f := range c.trames {
		p := decodeOne(t, f)
		if p.Type != PacketDATA || p.HasFlag(FlagACK) {
			continue
		}
		buf = append(buf, p.Payload...)
		if p.FragmentID != 0 {
			continue
		}
		m, err := ParseRMC(s, buf)
		buf = nil
		if err == nil && m.Protocol == ProtocolNATTraversal && m.Method == MethodInitiateProbe {
			n++
		}
	}

	return n
}
