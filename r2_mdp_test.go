package nex

// Correctifs de la relecture r2 (securite, coeur), arenes. Chaque test echouait avant son
// correctif :
//   - TestR2MdpForceBrutePar0x28 : frein de 0x6D.0x27 epuise, le mot de passe d'une arene
//     etait trouve par 0x6D.0x28 (« 7391 » en 7392 essais, intrus admis) ;
//   - TestAreneMdpEssaisBornesPar0x28 : 0x28 seul, sans frein du tout ;
//   - TestAutoSansSessionProtegee : option absente (SSBU : jamais d'arene par 0x28).
// PID fictifs 18009xxxxx, adresses de documentation (192.0.2.0/24, 198.51.100.0/24).

import (
	"fmt"
	"testing"
	"time"
)

// auto28 : AutoMatchmakeWithParam_Postpone avec ce mot de passe. OpenParticipation a false :
// la session ouverte par un essai sans succes n'est jamais candidate ensuite.
func (b *bancArene) auto28(c *Connection, mode uint32, mdp string) MatchmakeSession {
	b.t.Helper()
	p := &AutoMatchmakeParam{Session: MatchmakeSession{
		Gathering:         Gathering{MaxParticipants: 8},
		GameMode:          mode,
		OpenParticipation: false,
		Attribs:           []uint32{0, 0, 0, 0, 0, 0},
		UserPassword:      mdp,
	}}
	body := NewStreamOut(b.s)
	body.Add(p)
	rep := b.ext(c, NewRMCRequest(b.s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 9, body.Bytes()))
	if rep == nil || rep.IsError {
		b.t.Fatalf("0x28 %q : %+v", mdp, rep)
	}
	var out MatchmakeSession
	NewStreamIn(rep.Body, b.s).Extract(&out)
	return out
}

// niveauPID : refus en cours dans le seau de ce PID (mdp_essais.go).
func (b *bancArene) niveauPID(pid uint64) float64 {
	b.mm.mu.Lock()
	defer b.mm.mu.Unlock()
	if s := b.mm.mdpSeaux[fmt.Sprintf("pid:%d", pid)]; s != nil {
		return s.niveau
	}
	return 0
}

func TestR2MdpForceBrutePar0x28(t *testing.T) {
	_ = horlogeMdp(t) // horloge figee : aucune fuite des seaux pendant le test
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	const vrai = "7391"
	arene := b.creer(hote, 0x51, vrai)

	intrus := newTestConn(b.ep, 1800900277, "192.0.2.77:1")
	// 1) Le frein de 0x27 est epuise d'abord (5 refus) : 0x27 refuse alors meme le bon
	// (0x800300C9 ; 0x800300D5 avant le correctif, sans comparaison non plus).
	for i := 0; i < 5; i++ {
		b.rejoindre(intrus, arene.ID, "0000")
	}
	if r := b.rejoindre(intrus, arene.ID, vrai); (!refusFreine(r) && !refusMdp(r)) || dansArene(b, arene.ID, intrus.PID) {
		t.Fatalf("pre-condition : 0x27 devait etre freine, recu %+v", r)
	}
	// 2) Puis 0x6D.0x28 avec chaque code a 4 chiffres.
	debut := time.Now()
	trouve, essais := "", 0
	for i := 0; i < 10000; i++ {
		mdp := fmt.Sprintf("%04d", i)
		essais++
		if s := b.auto28(intrus, 0x51, mdp); s.ID == arene.ID {
			trouve = mdp
			break
		}
	}
	t.Logf("0x6D.0x28 : %d essais en %v, mot de passe trouve = %q, intrus dans l'arene = %v",
		essais, time.Since(debut).Round(time.Millisecond), trouve, dansArene(b, arene.ID, intrus.PID))
	if trouve != "" || dansArene(b, arene.ID, intrus.PID) {
		t.Fatalf("FORCE BRUTE PAR 0x6D.0x28 : %q trouve en %d essais, frein de 0x27 epuise, intrus admis dans l'arene protegee", trouve, essais)
	}
}

func TestAreneMdpEssaisBornesPar0x28(t *testing.T) {
	maintenant := horlogeMdp(t)
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	const vrai = "7391"
	arene := b.creer(hote, 0x51, vrai)
	// Deux autres arenes protegees du meme GameMode : un essai les compare toutes, un seul
	// refus est note par requete.
	b.creer(newTestConn(b.ep, 1800900281, "192.0.2.81:1"), 0x51, "11111111")
	b.creer(newTestConn(b.ep, 1800900282, "192.0.2.82:1"), 0x51, "22222222")

	intrus := newTestConn(b.ep, 1800900278, "192.0.2.78:1")
	b.auto28(intrus, 0x51, "0000")
	if n := b.niveauPID(intrus.PID); n != 1 {
		t.Fatalf("un essai 0x28 face a 3 arenes protegees : %v refus notes, attendu 1", n)
	}
	for i := 1; i < 10000; i++ {
		mdp := fmt.Sprintf("%04d", i)
		if mdp == vrai {
			continue
		}
		if s := b.auto28(intrus, 0x51, mdp); s.ID == arene.ID {
			t.Fatalf("essai %q admis dans l'arene", mdp)
		}
	}
	if n := b.niveauPID(intrus.PID); n > mdpCapPID {
		t.Fatalf("seau du PID a %v, au-dela de sa capacite %v : des essais freines ont ete compares", n, mdpCapPID)
	}
	if s := b.auto28(intrus, 0x51, vrai); s.ID == arene.ID || dansArene(b, arene.ID, intrus.PID) {
		t.Fatalf("bon mot de passe par 0x28 apres 9999 refus : admis, attendu un essai freine sans comparaison")
	}
	// Seaux communs a 0x27 et 0x28.
	if r := b.rejoindre(intrus, arene.ID, vrai); !refusFreine(r) {
		t.Fatalf("0x27 apres les refus de 0x28 : %+v, attendu 0x800300C9", r)
	}
	// Le palier ne dure pas : un essai de plus par mdpFuitePID.
	*maintenant = maintenant.Add(mdpFuitePID)
	if s := b.auto28(intrus, 0x51, vrai); s.ID != arene.ID || !dansArene(b, arene.ID, intrus.PID) {
		t.Fatalf("bon mot de passe par 0x28 apres la fuite d'un essai : session %d, attendu l'arene %d", s.ID, arene.ID)
	}

	// Un joueur qui se trompe deux fois entre au troisieme essai.
	joueur := newTestConn(b.ep, 1800900279, "198.51.100.79:1")
	for i := 0; i < 2; i++ {
		b.auto28(joueur, 0x51, "1234")
	}
	if s := b.auto28(joueur, 0x51, vrai); s.ID != arene.ID {
		t.Fatalf("bon mot de passe au 3e essai par 0x28 : session %d, attendu l'arene %d", s.ID, arene.ID)
	}
	// Sans mot de passe : jamais dans une arene protegee, et aucun refus note.
	public := newTestConn(b.ep, 1800900280, "198.51.100.80:1")
	if s := b.auto28(public, 0x51, ""); s.ID == arene.ID {
		t.Fatalf("0x28 sans mot de passe admis dans l'arene protegee")
	}
	if n := b.niveauPID(public.PID); n != 0 {
		t.Fatalf("0x28 sans mot de passe : %v refus notes, attendu 0", n)
	}
}

func TestAutoSansSessionProtegee(t *testing.T) {
	_ = horlogeMdp(t)
	b := nouveauBancArene(t)
	b.mm.AutoSansSessionProtegee = true
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	arene := b.creer(hote, 0x51, mdpArene)

	joueur := newTestConn(b.ep, pidBonMdp, "192.0.2.2:1")
	if s := b.auto28(joueur, 0x51, mdpArene); s.ID == arene.ID || dansArene(b, arene.ID, joueur.PID) {
		t.Fatalf("AutoSansSessionProtegee : 0x28 avec le bon mot de passe est entre dans l'arene")
	}
	if n := b.niveauPID(joueur.PID); n != 0 {
		t.Fatalf("aucune comparaison attendue, %v refus notes", n)
	}
	// L'arene se rejoint toujours par 0x27.
	if r := b.rejoindre(joueur, arene.ID, mdpArene); r == nil || r.IsError || !dansArene(b, arene.ID, joueur.PID) {
		t.Fatalf("0x27 avec le bon mot de passe : %+v", r)
	}
	// Une session ouverte sans mot de passe reste trouvee par 0x28.
	ouverte := b.creer(newTestConn(b.ep, pidArenePublic, "192.0.2.5:1"), 0x52, "")
	if s := b.auto28(newTestConn(b.ep, pidSansMdp, "192.0.2.4:1"), 0x52, ""); s.ID != ouverte.ID {
		t.Fatalf("session ouverte : 0x28 a rendu %d, attendu %d", s.ID, ouverte.ID)
	}
}
