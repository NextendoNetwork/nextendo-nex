package nex

import (
	"testing"
	"time"
)

// Pair deconnecte entre le sondage et le verdict : sa trace donne la paire relayee (verdict
// non compte) et la classe (0515 : un echec suffit, et le pair est mis en cause).
func TestVerdictVersUnPairParti(t *testing.T) {
	resetBesoins()
	resetVues()
	t.Cleanup(func() { resetBesoins(); resetVues() })
	ep := NewEndpoint(testSettings())

	// Paire relayee, hote parti avant le verdict (prod 17/10 01:24:16Z) : rien n est compte.
	local := connNATm(ep, "198.51.100.31:1", 1800900181, 1)
	hote := connNATm(ep, "198.51.100.32:1", 1800900182, 1)
	NoterPaireRelayee(local.PID, hote.PID)
	hote.Close()
	if ep.FindConnectionByID(hote.ID) != nil {
		t.Fatal("l hote doit etre retire")
	}
	verdictNAT(local, hote.ID, false)
	verdictNAT(local, hote.ID, false)
	if BesoinDeRelais(local.PID) {
		t.Fatal("verdicts d une paire relayee dont l hote est parti : rien ne doit etre compte")
	}

	// Paire directe, pair natm 2 parti : classe 0515 reconstituee, un echec suffit.
	strict := connNATm(ep, "198.51.100.33:1", 1800900183, 2)
	strict.Close()
	verdictNAT(local, strict.ID, false)
	if !BesoinDeRelais(local.PID) {
		t.Fatal("echec 0515 vers un pair parti : un echec doit suffire")
	}
	if !BesoinDeRelais(strict.PID) {
		t.Fatal("echec 0515 vers un pair parti : le pair natm 2 doit etre mis en cause")
	}

	// Trace expiree : classe « ? », seuil de deux (comportement d avant).
	resetBesoins()
	vieux := connNATm(ep, "198.51.100.34:1", 1800900184, 2)
	vieux.Close()
	ep.partisMu.Lock()
	p := ep.partis[vieux.ID]
	p.le = time.Now().Add(-relayVuTTL - time.Second)
	ep.partis[vieux.ID] = p
	ep.partisMu.Unlock()
	if _, ok := ep.connexionPartie(vieux.ID); ok {
		t.Fatal("trace expiree rendue")
	}
	verdictNAT(local, vieux.ID, false)
	if BesoinDeRelais(local.PID) || BesoinDeRelais(vieux.PID) {
		t.Fatal("trace expiree : un seul echec ne doit rien rendre eligible")
	}
}

// La trace est bornee (partisMax) et le menage retire les traces expirees.
func TestTraceDesPartisBornee(t *testing.T) {
	ep := NewEndpoint(testSettings())
	c := NewConnection(ep, "198.51.100.35:1", func([]byte) {})
	ep.registerConnection(c)
	c.Close()
	ep.partisMu.Lock()
	for i := uint32(0); len(ep.partis) < partisMax; i++ {
		ep.partis[1<<20+i] = connPartie{le: time.Now()}
	}
	ep.partisMu.Unlock()
	d := NewConnection(ep, "198.51.100.36:1", func([]byte) {})
	ep.registerConnection(d)
	d.Close()
	if _, ok := ep.connexionPartie(d.ID); ok {
		t.Fatal("trace pleine : la connexion suivante ne doit pas etre gardee")
	}
	ep.partisMu.Lock()
	for id, p := range ep.partis {
		p.le = time.Now().Add(-relayVuTTL - time.Minute)
		ep.partis[id] = p
	}
	ep.partisMenage = time.Time{}
	ep.partisMu.Unlock()
	f := NewConnection(ep, "198.51.100.37:1", func([]byte) {})
	ep.registerConnection(f)
	f.Close()
	ep.partisMu.Lock()
	n := len(ep.partis)
	ep.partisMu.Unlock()
	if n != 1 {
		t.Fatalf("apres menage : %d trace(s), attendu 1", n)
	}
}

// Console qui sonde ses pairs sans jamais faire de ReplaceURL (stations du Register,
// natm=0) : son natm vient de la station qu elle fait sonder. Prod 17/10 01:59:36Z : echec
// 0515 compte en classe « ? », au seuil de deux.
func TestNatmDeLaStationSondee(t *testing.T) {
	resetBesoins()
	resetVues()
	t.Cleanup(func() { resetBesoins(); resetVues() })
	s := testSettings()
	ep := NewEndpoint(s)
	sansReplace := connNATm(ep, "198.51.100.41:1", 1800900191, 0)
	strict := connNATm(ep, "198.51.100.42:1", 1800900192, 2)
	if natmStation(sansReplace) != 0 {
		t.Fatal("natm attendu inconnu avant le sondage")
	}
	NATTraversalHandler()(sansReplace, probeReqN(s, 3, strict, 1,
		"prudp:/address=198.51.100.41;port=52887;natf=17;natm=1;probeinit=0;RVCID=1"))
	if got := classePia(sansReplace, strict); got != "0515" {
		t.Fatalf("classe %s, attendu 0515", got)
	}
	verdictNAT(sansReplace, strict.ID, false)
	if !BesoinDeRelais(sansReplace.PID) {
		t.Fatal("echec 0515 : un echec doit suffire")
	}
	// Une station de ReplaceURL garde la priorite.
	u := NewStationURL("prudp")
	u.SetInt("natm", 2)
	sansReplace.SetStations([]*StationURL{u})
	if natmStation(sansReplace) != 2 {
		t.Fatal("la station du ReplaceURL doit primer")
	}
}
