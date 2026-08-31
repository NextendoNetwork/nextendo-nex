package nex

import (
	"testing"
	"time"
)

func resetVues() {
	vuMu.Lock()
	vues = map[[2]uint64]time.Time{}
	vuMenage = time.Time{}
	vuMu.Unlock()
}

// verdictPercage fabrique un ReportNATTraversalResult tel que la console l envoie :
// (u32 cid, bool resultat, u32 rtt).
func verdictPercage(t *testing.T, conn *Connection, cid uint32, reussi bool) {
	t.Helper()
	body := NewStreamOut(conn.Settings)
	body.U32(cid)
	body.Bool(reussi)
	body.U32(0)
	req := NewRMCRequest(conn.Settings, ProtocolNATTraversal, MethodReportNATTraversalResult, 1, body.Bytes())
	if resp := NATTraversalHandler()(conn, req); resp.IsError {
		t.Fatalf("ReportNATTraversalResult a echoue: %+v", resp)
	}
}

// TestVerdictDUnePaireRelayeeNAlimentePasLEligibilite : c est LE defaut qui a transforme le
// relais selectif en relais global. Une paire relayee n a plus de chemin direct, donc son
// verdict est FAILED par construction ; le compter, c est se donner raison tout seul.
func TestVerdictDUnePaireRelayeeNAlimentePasLEligibilite(t *testing.T) {
	resetVues()
	s := testSettings()
	ep := NewEndpoint(s)

	pair := NewConnection(ep, "2.2.2.2:1", func([]byte) {})
	pair.PID = 900002
	ep.registerConnection(pair)

	moi := NewConnection(ep, "1.1.1.1:1", func([]byte) {})
	moi.PID = 900001
	ep.registerConnection(moi)

	NoterPaireRelayee(moi.PID, pair.PID)

	verdictPercage(t, moi, pair.ID, false)
	verdictPercage(t, moi, pair.ID, false)

	if BesoinDeRelais(moi.PID) {
		t.Fatal("un verdict de paire relayee a nourri l eligibilite — la boucle est toujours la")
	}
}

// TestVerdictDirectAlimenteToujoursLEligibilite : le garde-fou inverse. Trop supprimer et
// plus personne ne devient jamais eligible, donc le relais ne sert plus a rien.
func TestVerdictDirectAlimenteToujoursLEligibilite(t *testing.T) {
	resetVues()
	s := testSettings()
	ep := NewEndpoint(s)

	pair := NewConnection(ep, "2.2.2.2:1", func([]byte) {})
	pair.PID = 900012
	ep.registerConnection(pair)

	moi := NewConnection(ep, "1.1.1.1:1", func([]byte) {})
	moi.PID = 900011
	ep.registerConnection(moi)

	t.Cleanup(func() {
		besoinMu.Lock()
		delete(besoins, moi.PID)
		besoinMu.Unlock()
	})

	verdictPercage(t, moi, pair.ID, false)
	verdictPercage(t, moi, pair.ID, false)

	if !BesoinDeRelais(moi.PID) {
		t.Fatal("deux echecs directs doivent rendre eligible")
	}
}

// TestPaireRelayeeExpire : au-dela du TTL, le joueur doit redevenir mesurable, sinon
// l eligibilite ne peut plus jamais decroitre.
func TestPaireRelayeeExpire(t *testing.T) {
	resetVues()
	NoterPaireRelayee(1, 2)
	if !PaireRelayee(2, 1) {
		t.Fatal("la paire doit etre reconnue dans les deux sens")
	}

	vuMu.Lock()
	vues[clePaire(1, 2)] = time.Now().Add(-relayVuTTL - time.Minute)
	vuMu.Unlock()

	if PaireRelayee(1, 2) {
		t.Fatal("une paire relayee il y a longtemps ne doit plus masquer les verdicts")
	}
}
