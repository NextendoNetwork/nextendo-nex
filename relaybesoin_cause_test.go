package nex

import (
	"testing"
	"time"
)

// connNATm : une connexion enregistree dont la station annonce ce natm.
func connNATm(ep *Endpoint, addr string, pid uint64, natm int) *Connection {
	c := NewConnection(ep, addr, func([]byte) {})
	c.PID = pid
	u := NewStationURL("prudp")
	u.Set("address", "10.0.0.1")
	u.SetInt("natm", natm)
	c.SetStations([]*StationURL{u})
	ep.registerConnection(c)

	return c
}

// verdictNAT : ReportNATTraversalResult de c sur son percage vers la connexion d id cid.
func verdictNAT(c *Connection, cid uint32, reussi bool) {
	s := c.Settings
	b := NewStreamOut(s)
	b.U32(cid)
	b.Bool(reussi)
	b.U32(0)
	logNATResult(c, NewRMCRequest(s, ProtocolNATTraversal, MethodReportNATTraversalResult, 1, b.Bytes()))
}

// Un echec direct 0515 (rapporteur natm 1, pair natm 2) rend eligible le rapporteur ET le
// pair, sans toucher a l historique du pair (essais, reussites). Les autres cas ne mettent
// pas le pair en cause : succes, 0516 (le rapporteur est deja le cote natm 2), 0513, paire
// passee par le relais.
func TestEchec0515MetEnCauseLePair(t *testing.T) {
	resetBesoins()
	resetVues()
	t.Cleanup(func() { resetBesoins(); resetVues() })
	ep := NewEndpoint(testSettings())
	local := connNATm(ep, "198.51.100.11:1", 1800900151, 1)
	strict := connNATm(ep, "198.51.100.12:1", 1800900152, 2)

	verdictNAT(local, strict.ID, false)
	if !BesoinDeRelais(local.PID) || !BesoinDeRelais(strict.PID) {
		t.Fatalf("0515 : rapporteur %v, pair natm 2 %v, attendu les deux eligibles",
			BesoinDeRelais(local.PID), BesoinDeRelais(strict.PID))
	}
	besoinMu.Lock()
	e := *besoins[strict.PID]
	besoinMu.Unlock()
	if e.essais != 0 || e.reussites != 0 || e.echecs != 1 || !e.strict {
		t.Fatalf("pair : %+v, attendu un echec strict sans essai", e)
	}
	// Un succes direct du pair efface sa marque, comme pour un rapporteur.
	NotePercageClasse(strict.PID, true, "0516")
	if BesoinDeRelais(strict.PID) {
		t.Fatal("un succes direct du pair doit effacer la mise en cause")
	}

	for _, cas := range []struct {
		nom          string
		natmL, natmP int
		reussi       bool
		relayee      bool
	}{
		{"succes 0515", 1, 2, true, false},
		{"echec 0516", 2, 1, false, false},
		{"echec 0513", 1, 1, false, false},
		{"echec 0515 relaye", 1, 2, false, true},
	} {
		resetBesoins()
		resetVues()
		l := connNATm(ep, "198.51.100.13:1", 1800900153, cas.natmL)
		p := connNATm(ep, "198.51.100.14:1", 1800900154, cas.natmP)
		if cas.relayee {
			NoterPaireRelayee(l.PID, p.PID)
		}
		verdictNAT(l, p.ID, cas.reussi)
		if BesoinDeRelais(p.PID) {
			t.Fatalf("%s : le pair ne doit pas etre mis en cause", cas.nom)
		}
	}
}

// Le menage oublie une mise en cause expiree (aucun essai a garder pour CandidatsRelais),
// et garde comme avant un rapporteur qui n a jamais reussi.
func TestMenageOublieLesMisesEnCause(t *testing.T) {
	resetBesoins()
	t.Cleanup(resetBesoins)
	NoteCauseStricte(1800900161, 1800900162)
	NotePercageClasse(1800900163, false, "0513")
	besoinMu.Lock()
	for _, e := range besoins {
		e.dernier = time.Now().Add(-besoinFenetre - time.Minute)
	}
	besoinNet = time.Time{}
	besoinMu.Unlock()
	NotePercageClasse(1800900164, false, "0513") // declenche le menage
	besoinMu.Lock()
	_, cause := besoins[1800900161]
	_, rapporteur := besoins[1800900163]
	besoinMu.Unlock()
	if cause || !rapporteur {
		t.Fatalf("apres menage : mise en cause gardee=%v (attendu non), rapporteur garde=%v (attendu oui)", cause, rapporteur)
	}
	NoteCauseStricte(0, 1800900162)
	besoinMu.Lock()
	_, zero := besoins[0]
	besoinMu.Unlock()
	if zero {
		t.Fatal("PID 0 ne doit jamais etre note")
	}
}

// Un verdict relaye de classe 0516 repousse l echeance d un joueur eligible en NAT strict,
// sans rien compter ; un verdict relaye d une autre classe, ou un joueur eligible sans echec
// strict, ne la repousse pas.
func TestVerdictRelaye0516ProlongeLEligibilite(t *testing.T) {
	resetBesoins()
	resetVues()
	t.Cleanup(func() { resetBesoins(); resetVues() })
	ep := NewEndpoint(testSettings())
	vieillir := func(pid uint64, d time.Duration) {
		besoinMu.Lock()
		besoins[pid].dernier = time.Now().Add(-d)
		besoinMu.Unlock()
	}

	natm2 := connNATm(ep, "198.51.100.21:1", 1800900171, 2)
	natm1 := connNATm(ep, "198.51.100.22:1", 1800900172, 1)
	NotePercageClasse(natm2.PID, false, "0516")
	vieillir(natm2.PID, besoinFenetre-time.Minute)
	NoterPaireRelayee(natm2.PID, natm1.PID)
	verdictNAT(natm2, natm1.ID, true)
	besoinMu.Lock()
	e := *besoins[natm2.PID]
	besoinMu.Unlock()
	if time.Since(e.dernier) > time.Minute {
		t.Fatalf("verdict relaye 0516 : echeance non repoussee (dernier il y a %v)", time.Since(e.dernier))
	}
	if e.echecs != 1 || e.essais != 1 || e.reussites != 0 || !e.strict {
		t.Fatalf("verdict relaye compte : %+v", e)
	}
	if !BesoinDeRelais(natm2.PID) {
		t.Fatal("toujours eligible attendu")
	}

	// Sans repousse, l eligibilite expire : on rejoue avec un verdict relaye 0513.
	resetBesoins()
	autre := connNATm(ep, "198.51.100.23:1", 1800900173, 1)
	NotePercageClasse(natm1.PID, false, "0515")
	vieillir(natm1.PID, besoinFenetre-time.Minute)
	NoterPaireRelayee(natm1.PID, autre.PID)
	verdictNAT(natm1, autre.ID, false)
	besoinMu.Lock()
	age := time.Since(besoins[natm1.PID].dernier)
	besoinMu.Unlock()
	if age < besoinFenetre-2*time.Minute {
		t.Fatal("verdict relaye 0513 : l echeance ne doit pas etre repoussee")
	}

	// Eligible par deux echecs ordinaires (sans marque stricte) : pas de repousse.
	resetBesoins()
	NotePercageClasse(natm2.PID, false, "0513")
	NotePercageClasse(natm2.PID, false, "0513")
	vieillir(natm2.PID, besoinFenetre-time.Minute)
	verdictNAT(natm2, natm1.ID, false)
	besoinMu.Lock()
	age = time.Since(besoins[natm2.PID].dernier)
	besoinMu.Unlock()
	if age < besoinFenetre-2*time.Minute {
		t.Fatal("eligible sans echec strict : l echeance ne doit pas etre repoussee")
	}
}
