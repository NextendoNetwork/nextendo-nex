package nex

import (
	"testing"
	"time"
)

func resetBesoins() {
	besoinMu.Lock()
	besoins = map[uint64]*besoinEtat{}
	besoinNet = time.Time{}
	besoinMu.Unlock()
}

// TestUnSeulEchecNeSuffitPas : un echec isole arrive a tout le monde (pair parti, paquet
// perdu) et ne justifie pas de detourner tout le trafic d un joueur.
func TestUnSeulEchecNeSuffitPas(t *testing.T) {
	resetBesoins()
	NotePercage(1, false)
	if BesoinDeRelais(1) {
		t.Fatal("un seul echec ne doit pas rendre eligible")
	}
	NotePercage(1, false)
	if !BesoinDeRelais(1) {
		t.Fatal("deux echecs doivent rendre eligible")
	}
}

// TestUnSuccesEffaceLArdoise : le joueur vient de prouver qu il n a pas besoin du relais.
// Sans cela, un joueur qui branche un cable resterait detourne pendant un quart d heure.
func TestUnSuccesEffaceLArdoise(t *testing.T) {
	resetBesoins()
	NotePercage(2, false)
	NotePercage(2, false)
	if !BesoinDeRelais(2) {
		t.Fatal("doit etre eligible apres deux echecs")
	}
	NotePercage(2, true)
	if BesoinDeRelais(2) {
		t.Fatal("un succes doit effacer l eligibilite")
	}
}

// TestEchecsTropVieux : au-dela de la fenetre, un echec ne dit plus rien de l etat actuel
// du reseau du joueur.
func TestEchecsTropVieux(t *testing.T) {
	resetBesoins()
	NotePercage(3, false)
	NotePercage(3, false)

	besoinMu.Lock()
	besoins[3].dernier = time.Now().Add(-besoinFenetre - time.Minute)
	besoinMu.Unlock()

	if BesoinDeRelais(3) {
		t.Fatal("un echec hors fenetre ne doit plus rendre eligible")
	}
}

// TestRelaisRefuseSansBesoin : la porte d entree elle-meme. Deux joueurs qui n ont jamais
// echoue ne doivent PAS recevoir de port de relais.
func TestRelaisRefuseSansBesoin(t *testing.T) {
	resetBesoins()
	SetPairRelay("127.0.0.1", 39500, 50)
	defer SetPairRelay("", 0, 0)
	// Autorises : ce test porte sur le BESOIN, pas sur l autorisation.
	volontairesPourTest(t, 10, 11)

	if _, _, ok := PairRelayFor(10, 11, "1.1.1.1", "2.2.2.2"); ok {
		t.Fatal("relais accorde a une paire sans besoin")
	}

	// Un seul des deux suffit : le percage est une operation a deux.
	NotePercage(11, false)
	NotePercage(11, false)
	if _, _, ok := PairRelayFor(10, 11, "1.1.1.1", "2.2.2.2"); !ok {
		t.Fatal("relais refuse alors qu un des deux en a besoin")
	}
}

// TestCandidatsRelaisSeulementCeuxQuiNeConnectentJamais : le critere des ajouts automatiques.
// Volontairement haut et incontestable — ces joueurs ne peuvent rien perdre puisqu ils ne
// jouent pas. Mesure du 2026-08-31 : 45 sur 113 dans ce cas.
func TestCandidatsRelaisSeulementCeuxQuiNeConnectentJamais(t *testing.T) {
	resetBesoins()

	// Jamais connecte, au-dela du seuil.
	for i := 0; i < jamaisConnecteSeuil; i++ {
		NotePercage(700, false)
	}
	// Echoue beaucoup mais connecte parfois : ce n est PAS un candidat.
	for i := 0; i < 20; i++ {
		NotePercage(701, false)
	}
	NotePercage(701, true)
	// Trop peu de tentatives pour conclure.
	for i := 0; i < jamaisConnecteSeuil-1; i++ {
		NotePercage(702, false)
	}

	got := CandidatsRelais()
	if len(got) != 1 || got[0] != 700 {
		t.Fatalf("candidats=%v, attendu [700]", got)
	}
}

// TestUnSuccesRetireDesCandidats : si son reseau s ameliore, il doit sortir tout seul.
func TestUnSuccesRetireDesCandidats(t *testing.T) {
	resetBesoins()
	for i := 0; i < jamaisConnecteSeuil; i++ {
		NotePercage(710, false)
	}
	if len(CandidatsRelais()) != 1 {
		t.Fatal("devait etre candidat")
	}

	NotePercage(710, true)
	if len(CandidatsRelais()) != 0 {
		t.Fatal("un succes doit le retirer des candidats")
	}
}

// TestHistoriqueSurvitAuMenage : le menage efface l ardoise glissante, mais pas la memoire de
// ceux qui n ont jamais reussi — sinon plus personne ne serait jamais repere.
func TestHistoriqueSurvitAuMenage(t *testing.T) {
	resetBesoins()
	for i := 0; i < jamaisConnecteSeuil; i++ {
		NotePercage(720, false)
	}

	besoinMu.Lock()
	besoins[720].dernier = time.Now().Add(-besoinFenetre - time.Hour)
	besoinNet = time.Time{}
	besoinMu.Unlock()

	// Un appel quelconque declenche le menage.
	NotePercage(999, false)

	if len(CandidatsRelais()) != 1 {
		t.Fatal("le menage a efface un joueur qui ne se connecte jamais")
	}
	if BesoinDeRelais(720) {
		t.Fatal("l ardoise glissante, elle, devait expirer")
	}
}

// TestUnEchecNATStrictSuffit : un seul echec de classe 0515 ou 0516 (natm 1 contre natm 2)
// rend eligible ; les autres classes gardent le seuil de deux.
func TestUnEchecNATStrictSuffit(t *testing.T) {
	resetBesoins()
	for i, classe := range []string{"0515", "0516"} {
		pid := uint64(1800900101 + i)
		NotePercageClasse(pid, false, classe)
		if !BesoinDeRelais(pid) {
			t.Fatalf("classe %s : un echec doit suffire", classe)
		}
	}
	for i, classe := range []string{"0513", "0514", "0583-0586", "?", ""} {
		pid := uint64(1800900111 + i)
		NotePercageClasse(pid, false, classe)
		if BesoinDeRelais(pid) {
			t.Fatalf("classe %q : un seul echec ne doit pas suffire", classe)
		}
		NotePercageClasse(pid, false, classe)
		if !BesoinDeRelais(pid) {
			t.Fatalf("classe %q : deux echecs doivent suffire", classe)
		}
	}
}

// TestNATStrictMelangeEtSucces : un echec ordinaire suivi d un echec strict rend eligible ;
// un succes direct efface tout, y compris la marque stricte ; une fenetre expiree aussi.
func TestNATStrictMelangeEtSucces(t *testing.T) {
	resetBesoins()
	const pid = 1800900121
	NotePercageClasse(pid, false, "0513")
	NotePercageClasse(pid, false, "0516")
	if !BesoinDeRelais(pid) {
		t.Fatal("echec ordinaire + echec strict : doit etre eligible")
	}
	NotePercageClasse(pid, true, "0516")
	if BesoinDeRelais(pid) {
		t.Fatal("un succes direct doit effacer l eligibilite")
	}
	NotePercageClasse(pid, false, "0513")
	if BesoinDeRelais(pid) {
		t.Fatal("la marque stricte doit avoir ete effacee par le succes")
	}

	resetBesoins()
	NotePercageClasse(pid, false, "0515")
	besoinMu.Lock()
	besoins[pid].dernier = time.Now().Add(-besoinFenetre - time.Minute)
	besoinMu.Unlock()
	if BesoinDeRelais(pid) {
		t.Fatal("un echec strict hors fenetre ne doit plus rendre eligible")
	}
	NotePercageClasse(pid, false, "0513")
	if BesoinDeRelais(pid) {
		t.Fatal("fenetre expiree : la marque stricte ne doit pas survivre")
	}
	if n := BesoinStats(); n != 0 {
		t.Fatalf("BesoinStats = %d, attendu 0", n)
	}
	NotePercageClasse(pid, false, "0513")
	if l := BesoinListe(); len(l) != 1 || l[0] != pid {
		t.Fatalf("BesoinListe = %v", l)
	}
}

// TestNotePercageCompatible : l ancienne signature garde le seuil de deux.
func TestNotePercageCompatible(t *testing.T) {
	resetBesoins()
	const pid = 1800900131
	NotePercage(pid, false)
	if BesoinDeRelais(pid) {
		t.Fatal("NotePercage : un echec ne doit pas suffire")
	}
	NotePercage(pid, false)
	if !BesoinDeRelais(pid) {
		t.Fatal("NotePercage : deux echecs doivent suffire")
	}
}

// TestVerdictNATStrictParLeHandler : chemin complet, ReportNATTraversalResult FAILED vers un
// pair natm 2 depuis une console natm 1 (classe 0515) rend le joueur eligible des le premier
// verdict ; vers un pair natm 1 (0513), il en faut deux.
func TestVerdictNATStrictParLeHandler(t *testing.T) {
	resetBesoins()
	s := testSettings()
	ep := NewEndpoint(s)
	avec := func(addr string, pid uint64, natm int) *Connection {
		c := NewConnection(ep, addr, func([]byte) {})
		c.PID = pid
		u := NewStationURL("prudp")
		u.Set("address", "10.0.0.1")
		u.SetInt("natm", natm)
		c.SetStations([]*StationURL{u})
		ep.registerConnection(c)
		return c
	}
	verdict := func(c, pair *Connection) {
		b := NewStreamOut(s)
		b.U32(pair.ID)
		b.Bool(false)
		b.U32(0)
		logNATResult(c, NewRMCRequest(s, ProtocolNATTraversal, MethodReportNATTraversalResult, 1, b.Bytes()))
	}

	local := avec("198.51.100.1:1", 1800900141, 1)
	strict := avec("198.51.100.2:1", 1800900142, 2)
	ouvert := avec("198.51.100.3:1", 1800900143, 1)
	if got := classePia(local, strict); got != "0515" {
		t.Fatalf("classe %s, attendu 0515", got)
	}
	verdict(local, strict)
	if !BesoinDeRelais(local.PID) {
		t.Fatal("un echec 0515 doit rendre eligible")
	}

	resetBesoins()
	verdict(local, ouvert)
	if BesoinDeRelais(local.PID) {
		t.Fatal("un echec 0513 ne doit pas suffire")
	}
	verdict(local, ouvert)
	if !BesoinDeRelais(local.PID) {
		t.Fatal("deux echecs 0513 doivent rendre eligible")
	}
}
