package nex

// Correctifs de la relecture r1 (coeur), arenes et sessions isolees. Chaque test echouait
// avant son correctif :
//   - TestAreneMdpEssaisBornes : mot de passe d'arene trouve par force brute (0x6D.0x27 sans
//     borne : « 7391 » trouve en 7392 essais) ;
//   - TestSessionIsoleeSansCleNiStations : la cle de session d'un match isole (ModeIsole)
//     sortait par 0x6D.0x34 avec le mot de code, les stations de l'hote par 0x15.0x29.
// PID fictifs 18009xxxxx, adresses de documentation (192.0.2.0/24, 198.51.100.0/24).

import (
	"fmt"
	"testing"
	"time"
)

func horlogeMdp(t *testing.T) *time.Time {
	t.Helper()
	ancienne := mdpHorloge
	maintenant := time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC)
	mdpHorloge = func() time.Time { return maintenant }
	t.Cleanup(func() { mdpHorloge = ancienne })
	return &maintenant
}

// refusFreine : refus d'un essai freine, sans comparaison (0x800300C9, « Impossible de
// rejoindre cette arene. »).
func refusFreine(r *RMCMessage) bool { return r != nil && r.IsError && r.Result == 0x800300C9 }

func dansArene(b *bancArene, gid uint32, pid uint64) bool {
	for _, p := range b.participants(gid) {
		if p == pid {
			return true
		}
	}
	return false
}

func TestAreneMdpEssaisBornes(t *testing.T) {
	maintenant := horlogeMdp(t)
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	const vrai = "7391"
	arene := b.creer(hote, 0x51, vrai)

	// Force brute : les 10 000 codes a 4 chiffres, le bon en dernier.
	intrus := newTestConn(b.ep, 1800900077, "192.0.2.77:1")
	for i := 0; i < 10000; i++ {
		mdp := fmt.Sprintf("%04d", i)
		if mdp == vrai {
			continue
		}
		if r := b.rejoindre(intrus, arene.ID, mdp); !refusMdp(r) && !refusFreine(r) {
			t.Fatalf("essai %q : %+v, attendu 0x800300D5 ou 0x800300C9", mdp, r)
		}
	}
	if r := b.rejoindre(intrus, arene.ID, vrai); !refusFreine(r) || dansArene(b, arene.ID, intrus.PID) {
		t.Fatalf("bon mot de passe apres 9999 refus : %+v, attendu 0x800300C9 sans comparaison (palier en cours)", r)
	}
	// Le palier ne dure pas : un essai de plus par mdpFuitePID.
	*maintenant = maintenant.Add(mdpFuitePID)
	if r := b.rejoindre(intrus, arene.ID, vrai); r == nil || r.IsError || !dansArene(b, arene.ID, intrus.PID) {
		t.Fatalf("bon mot de passe apres la fuite d'un essai : %+v", r)
	}

	// Des le 6e essai de suite, plus de comparaison : le bon mot de passe est refuse.
	freine := newTestConn(b.ep, 1800900078, "198.51.100.78:1")
	for i := 0; i < 5; i++ {
		if r := b.rejoindre(freine, arene.ID, "0000"); !refusMdp(r) {
			t.Fatalf("refus %d : %+v", i+1, r)
		}
	}
	if r := b.rejoindre(freine, arene.ID, vrai); !refusFreine(r) || dansArene(b, arene.ID, freine.PID) {
		t.Fatalf("6e essai (le bon) : %+v, attendu un refus sans comparaison", r)
	}

	// Un joueur qui se trompe deux fois entre au troisieme essai.
	joueur := newTestConn(b.ep, 1800900079, "198.51.100.79:1")
	for i := 0; i < 2; i++ {
		if r := b.rejoindre(joueur, arene.ID, "1234"); !refusMdp(r) {
			t.Fatalf("faute de frappe %d : %+v", i+1, r)
		}
	}
	if r := b.rejoindre(joueur, arene.ID, vrai); r == nil || r.IsError || !dansArene(b, arene.ID, joueur.PID) {
		t.Fatalf("bon mot de passe au 3e essai : %+v", r)
	}

	// Un PID different a chaque essai depuis la meme IP : freine par le seau de l'IP.
	arene2 := b.creer(hote, 0x52, vrai)
	for i := 0; i < int(mdpCapIP); i++ {
		c := newTestConn(b.ep, uint64(1800900100+i), "198.51.100.99:1")
		if r := b.rejoindre(c, arene2.ID, "0000"); !refusMdp(r) {
			t.Fatalf("IP partagee, refus %d : %+v", i+1, r)
		}
	}
	tournant := newTestConn(b.ep, 1800900199, "198.51.100.99:2")
	if r := b.rejoindre(tournant, arene2.ID, vrai); !refusFreine(r) || dansArene(b, arene2.ID, tournant.PID) {
		t.Fatalf("PID neuf, IP freinee, bon mot de passe : %+v, attendu un refus", r)
	}
	*maintenant = maintenant.Add(mdpFuiteIP)
	if r := b.rejoindre(tournant, arene2.ID, vrai); r == nil || r.IsError {
		t.Fatalf("apres la fuite d'un essai de l'IP : %+v", r)
	}

	// Le proprietaire n'est jamais freine.
	if r := b.rejoindre(hote, arene.ID, ""); r == nil || r.IsError {
		t.Fatalf("hote : %+v", r)
	}
}

// Session isolee (match de tournoi) : ni cle par la recherche par mot de code, ni stations
// pour qui n'y est pas. Une session ordinaire trouvee par code (Dodo Code d'ACNH) garde sa cle.
func TestSessionIsoleeSansCleNiStations(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	mm := NewMatchmaking()
	ext, mmh := mm.ExtensionHandler(), mm.MatchMakingHandler()

	hote := newTestConn(ep, 1800900201, "192.0.2.201:1")
	st := NewStationURL("prudp")
	st.Set("address", "192.0.2.201")
	st.SetInt("RVCID", int(hote.ID))
	hote.SetStations([]*StationURL{st})
	adversaire := newTestConn(ep, 1800900202, "192.0.2.202:1")
	intrus := newTestConn(ep, 1800900203, "192.0.2.203:1")

	auto := func(c *Connection, mode uint32, code string) MatchmakeSession {
		p := &AutoMatchmakeParam{Session: MatchmakeSession{
			Gathering: Gathering{MaxParticipants: 2, MinParticipants: 2}, GameMode: mode, Codeword: code,
			OpenParticipation: true, Attribs: []uint32{0, 0, 0, 0, 0, 0},
		}}
		o := NewStreamOut(s)
		o.Add(p)
		var r MatchmakeSession
		NewStreamIn(ext(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodAutoMatchmakeWithParamPostpone, 1, o.Bytes())).Body, s).Extract(&r)
		return r
	}
	parcourir := func(c *Connection, code string) []MatchmakeSession {
		o := NewStreamOut(s)
		o.Add(&MatchmakeSessionSearchCriteria{Codeword: code})
		r := ext(c, NewRMCRequest(s, ProtocolMatchmakeExtension, MethodBrowseNoHolder, 2, o.Bytes()))
		in := NewStreamIn(r.Body, s)
		out := make([]MatchmakeSession, in.U32())
		for i := range out {
			in.Extract(&out[i])
		}
		return out
	}
	urls := func(c *Connection, gid uint32) int {
		o := NewStreamOut(s)
		o.U32(gid)
		r := mmh(c, NewRMCRequest(s, ProtocolMatchMaking, MethodGetSessionURLs, 3, o.Bytes()))
		return len(ReadList(NewStreamIn(r.Body, s), func(i *StreamIn) *StationURL { return i.StationURLValue() }))
	}

	const code = "T-0000000007-00002"
	match := auto(hote, 0x54000123, code)
	if j := auto(adversaire, 0x54000123, code); j.ID != match.ID || len(j.SessionKey) == 0 {
		t.Fatalf("adversaire : gid %d (attendu %d), cle %d octets : AutoMatchmake doit rendre la cle", j.ID, match.ID, len(j.SessionKey))
	}
	SecureConnectionHandler()(hote, replaceURLReq(s, 9, st))
	vues := parcourir(intrus, code)
	if len(vues) != 1 || vues[0].ID != match.ID {
		t.Fatalf("0x34 avec le mot de code : %d session(s)", len(vues))
	}
	if len(vues[0].SessionKey) != 0 {
		t.Errorf("0x34 avec le mot de code d'un match isole : cle de session de %d octets rendue", len(vues[0].SessionKey))
	}
	if n := urls(intrus, match.ID); n != 0 {
		t.Errorf("0x15.0x29 par un PID hors du match isole : %d station(s) de l'hote rendue(s)", n)
	}
	if n := urls(adversaire, match.ID); n != 1 {
		t.Errorf("0x15.0x29 par l'adversaire place : %d station(s), attendu 1", n)
	}

	// Temoin : session ordinaire trouvee par code, la cle reste (visite d'ile par Dodo Code).
	ile := auto(newTestConn(ep, 1800900204, "192.0.2.204:1"), 3, "ABCDE")
	if v := parcourir(intrus, "ABCDE"); len(v) != 1 || v[0].ID != ile.ID || len(v[0].SessionKey) == 0 {
		t.Errorf("session ordinaire par code : %+v, la cle doit rester", v)
	}
}
