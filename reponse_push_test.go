package nex

import (
	"encoding/hex"
	"testing"
)

// La charge relevee en prod (17/10, journalisee « requete ABANDONNEE (mode inattendu) »)
// est une reponse d'erreur au push InitiateProbe : protocole 3, erreur 0x80010002, call
// 0xFFFF001D. dispatchRMC la reconnait comme reponse : aucun gestionnaire, rien renvoye.
func TestReponseConsoleAuPushNonServie(t *testing.T) {
	charge, _ := hex.DecodeString("0a0000000300020001801d00ffff")
	s := testSettings()
	m, err := ParseRMC(s, charge)
	if err != nil || m.Mode != RMCResponse || !m.IsError || m.Protocol != ProtocolNATTraversal ||
		m.Result != 0x80010002 || m.CallID != 0xFFFF001D {
		t.Fatalf("decodage : err=%v %+v", err, m)
	}

	ep := NewEndpoint(s)
	appels := 0
	ep.Register(ProtocolNATTraversal, func(c *Connection, req *RMCMessage) *RMCMessage {
		appels++

		return natAck(c, req)
	})
	rmc := 0
	ep.OnRMC = func(*Connection, *RMCMessage) { rmc++ }
	envoyes := 0
	conn := NewConnection(ep, "192.0.2.62:1262", func(b []byte) {
		if p, _, err := DecodePackets(b); err == nil && len(p) == 1 && !p[0].HasFlag(FlagACK) {
			envoyes++
		}
	})
	conn.state.Store(stateConnected)
	conn.Feed(EncodePacket(dataPacket(charge, 2)))
	if appels != 0 || rmc != 0 || envoyes != 0 {
		t.Fatalf("reponse traitee comme une requete : %d appel(s), OnRMC %d, %d envoi(s)", appels, rmc, envoyes)
	}
}
