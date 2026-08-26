package nex

import (
	"os"
	"testing"
)

// Le client lit la reponse comme « une liste de profils, puis une liste de resultats »,
// et attend UN resultat par PID demande. Rendre un nombre different decale sa lecture
// et casse tout ce qui suit — d'ou ce test sur la correspondance.
func TestDataStoreGetUsersUnResultatParPID(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800001206

	demandes := []uint64{1800000001, 1800000002, 1800000003}
	inner := NewStreamOut(s)
	WriteList(inner, demandes, func(o *StreamOut, p uint64) { o.PID(p) })
	inner.U32(0) // option
	body := NewStreamOut(s)
	body.U8(0)
	body.Buffer(inner.Bytes())

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreGetUsers, 41, body.Bytes())
	resp := DataStoreSMM2Handler()(conn, req)
	if resp.IsError {
		t.Fatalf("GetUsers a echoue : %+v", resp)
	}

	in := NewStreamIn(resp.Body, s)
	users := ReadList(in, func(i *StreamIn) uint32 { return i.U32() })
	if len(users) != 0 {
		t.Fatalf("%d profils rendus, on n'en a aucun a servir", len(users))
	}
	results := ReadList(in, func(i *StreamIn) uint32 { return i.U32() })
	if len(results) != len(demandes) {
		t.Fatalf("%d resultats pour %d PID demandes", len(results), len(demandes))
	}
	for i, r := range results {
		if r != ResultDataStoreNotFound {
			t.Fatalf("resultat %d = 0x%08X, attendu DataStore::NotFound (0x%08X)",
				i, r, ResultDataStoreNotFound)
		}
	}
	if !in.EOF() {
		t.Fatal("octets en trop : le client exige eof() apres les deux listes")
	}
}

// Le corps vient du client. Une liste demesuree ne doit pas nous faire fabriquer une
// reponse demesuree.
func TestDataStoreGetUsersBorneLaDemande(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})

	trop := make([]uint64, 5000)
	inner := NewStreamOut(s)
	WriteList(inner, trop, func(o *StreamOut, p uint64) { o.PID(p) })
	inner.U32(0)
	body := NewStreamOut(s)
	body.U8(0)
	body.Buffer(inner.Bytes())

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreGetUsers, 42, body.Bytes())
	resp := DataStoreSMM2Handler()(conn, req)
	if resp.IsError {
		t.Fatalf("refus inattendu : %+v", resp)
	}
	in := NewStreamIn(resp.Body, s)
	ReadList(in, func(i *StreamIn) uint32 { return i.U32() })
	if n := len(ReadList(in, func(i *StreamIn) uint32 { return i.U32() })); n > 256 {
		t.Fatalf("%d resultats rendus, la borne etait 256", n)
	}
}

// Une methode 0x73 qu'on ne traite pas doit etre refusee explicitement, pas avalee :
// c'est le journal de ces refus qui nous dira quoi ecrire ensuite.
func TestDataStoreMethodeNonImplementeeRefusee(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, 73 /* SearchCoursesLatest */, 43, nil)
	if resp := DataStoreSMM2Handler()(conn, req); !resp.IsError {
		t.Fatal("une methode non implementee doit etre refusee")
	}
}

// Le contrat de RegisterUser est entierement du cote LECTURE : la reponse est vide,
// mais la requete doit etre consommee dans l'ordre EXACT ou elle est ecrite. Lire un
// champ au mauvais endroit decale tous les suivants, et on enregistrerait un profil
// plausible mais faux — le pire des resultats, parce qu'il ne se signale pas.
func TestDataStoreRegisterUserLitDansLOrdre(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800001206

	mii := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02}

	// On fabrique la requete AVEC ses en-tetes de structure, comme la console les
	// envoie. La premiere version de ce test les omettait : il passait au vert sur un
	// format qui n'existe pas, pendant que la vraie console echouait en « unexpected
	// EOF ». Un test qui valide autre chose que le reel est pire qu'aucun test.
	unk := NewStreamOut(s)
	unk.U16(1)
	unk.U16(2)
	unk.U16(3)
	unk.U16(4)

	inner := NewStreamOut(s)
	inner.String("Juanbrew")
	inner.U8(0) // version de UnknownStruct1
	inner.Buffer(unk.Bytes())
	inner.QBuffer(mii)
	inner.U8(7) // langue
	inner.String("ES")
	inner.String("appareil-de-test")

	body := NewStreamOut(s)
	body.U8(0) // version de RegisterUserParam
	body.Buffer(inner.Bytes())

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreRegisterUser, 61, body.Bytes())
	resp := DataStoreSMM2Handler()(conn, req)
	if resp.IsError {
		t.Fatalf("RegisterUser a echoue : %+v", resp)
	}
	if len(resp.Body) != 0 {
		t.Fatalf("corps de %d octets, la reponse doit etre vide", len(resp.Body))
	}

	p, ok := SMM2ProfilDe(conn.PID)
	if !ok {
		t.Fatal("le profil n'a pas ete enregistre")
	}
	if p.Nom != "Juanbrew" {
		t.Errorf("nom = %q", p.Nom)
	}
	if p.Pays != "ES" {
		t.Errorf("pays = %q", p.Pays)
	}
	if p.Langue != 7 {
		t.Errorf("langue = %d", p.Langue)
	}
	if p.Appareil != "appareil-de-test" {
		t.Errorf("appareil = %q", p.Appareil)
	}
	if string(p.Mii) != string(mii) {
		t.Errorf("Mii altere : %x", p.Mii)
	}
	if p.Unk != [4]uint16{1, 2, 3, 4} {
		t.Errorf("bloc de 4 u16 mal lu : %v", p.Unk)
	}
}

// Une requete tronquee ne doit RIEN enregistrer. Un demi-profil serait accepte en
// silence et mentirait ensuite a chaque lecture.
func TestDataStoreRegisterUserRefuseUneRequeteTronquee(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800009999

	inner := NewStreamOut(s)
	inner.String("CorteAqui") // et rien d'autre : la structure est tronquee

	body := NewStreamOut(s)
	body.U8(0)
	body.Buffer(inner.Bytes())

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreRegisterUser, 62, body.Bytes())
	if resp := DataStoreSMM2Handler()(conn, req); !resp.IsError {
		t.Fatal("une requete tronquee doit etre refusee")
	}
	if _, ok := SMM2ProfilDe(conn.PID); ok {
		t.Fatal("un profil a ete enregistre malgre une requete illisible")
	}
}

// Le cas qui compte maintenant : un profil EXISTE, il doit revenir. Et surtout, la
// correspondance doit tenir quand une partie des PID est connue et l'autre non — c'est
// la situation reelle des que deux joueurs se croisent.
func TestDataStoreGetUsersRendLesProfilsConnus(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800001206

	connu := uint64(1800004242)
	smm2Profils.Store(connu, SMM2Profil{
		PID: connu, Nom: "Juanjo", Pays: "FR", Langue: 2,
		Mii: []byte{1, 2, 3, 4}, Unk: [4]uint16{9, 8, 7, 6},
	})
	defer smm2Profils.Delete(connu)

	// Un inconnu AVANT et APRES le connu : si l'ordre se perd, ce test le voit.
	demandes := []uint64{1800000001, connu, 1800000002}
	inner := NewStreamOut(s)
	WriteList(inner, demandes, func(o *StreamOut, p uint64) { o.PID(p) })
	inner.U32(0)
	body := NewStreamOut(s)
	body.U8(0)
	body.Buffer(inner.Bytes())

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreGetUsers, 90, body.Bytes())
	resp := DataStoreSMM2Handler()(conn, req)
	if resp.IsError {
		t.Fatalf("GetUsers a echoue : %+v", resp)
	}

	in := NewStreamIn(resp.Body, s)
	if n := in.U32(); n != 1 {
		t.Fatalf("%d profils rendus, on en attendait 1", n)
	}
	// On relit le profil au format ou on l'a ecrit, pour verifier qu'il traverse intact.
	if s.StructHeader {
		_ = in.U8()
		in = in.Substream()
	}
	if pid := in.PID(); pid != connu {
		t.Fatalf("pid = %d, attendu %d", pid, connu)
	}
	code := in.String()
	if code == "" {
		t.Fatal("code Maker vide")
	}
	if nom := in.String(); nom != "Juanjo" {
		t.Fatalf("nom = %q", nom)
	}

	// La liste des resultats se lit dans le flux D'ORIGINE, pas dans le sous-flux.
	in2 := NewStreamIn(resp.Body, s)
	in2.U32() // count des profils
	if s.StructHeader {
		_ = in2.U8()
		_ = in2.Buffer()
	}
	if n := in2.U32(); n != uint32(len(demandes)) {
		t.Fatalf("%d resultats pour %d PID demandes", n, len(demandes))
	}
	attendu := []uint32{ResultDataStoreNotFound, 0, ResultDataStoreNotFound}
	for i, want := range attendu {
		if got := in2.U32(); got != want {
			t.Fatalf("resultat %d = 0x%08X, attendu 0x%08X (l'ordre de la demande doit etre conserve)", i, got, want)
		}
	}
}

// Le code Maker s'affiche a l'ecran et les joueurs se le communiquent : il doit etre
// STABLE pour un joueur donne. Le voir changer d'une session a l'autre serait pire que
// de ne pas en avoir.
func TestCodeMakerStable(t *testing.T) {
	a := codeMakerDe(1800001206)
	if a != codeMakerDe(1800001206) {
		t.Fatal("le code Maker change pour le meme joueur")
	}
	if a == codeMakerDe(1800001207) {
		t.Fatal("deux joueurs partagent le meme code Maker")
	}
	if len(a) != 9 {
		t.Fatalf("code de %d caracteres, attendu 9", len(a))
	}
}

// Le comportement qui debloque l'ecran du Mii : la console appelle GetUsers avec une
// liste VIDE pour reclamer SON PROPRE profil. Repondre « rien demande, rien rendu »
// etait exact et inutile — le jeu ne voyait jamais son compte.
func TestDataStoreGetUsersListeVideRendLeProfilDuJoueur(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800007777

	smm2Profils.Store(conn.PID, SMM2Profil{PID: conn.PID, Nom: "MoiMeme", Pays: "ES"})
	defer smm2Profils.Delete(conn.PID)

	inner := NewStreamOut(s)
	WriteList(inner, []uint64{}, func(o *StreamOut, p uint64) { o.PID(p) })
	inner.U32(0)
	body := NewStreamOut(s)
	body.U8(0)
	body.Buffer(inner.Bytes())

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreGetUsers, 99, body.Bytes())
	resp := DataStoreSMM2Handler()(conn, req)
	if resp.IsError {
		t.Fatalf("GetUsers a echoue : %+v", resp)
	}
	in := NewStreamIn(resp.Body, s)
	if n := in.U32(); n != 1 {
		t.Fatalf("%d profils rendus pour une liste vide, on attendait le sien", n)
	}
}

// Le vrai comportement de SMM2 : il reclame les profils par identifiant NSA, pas par
// notre PID. Les deux existent et ne coincident pas — c'est ce qui faisait echouer
// toutes les recherches alors que le profil etait bien enregistre.
func TestDataStoreProfilTrouveParNSA(t *testing.T) {
	const nsa = uint64(11389664994428258486)
	const pid = uint64(1800001206)

	RememberLoginName("11389664994428258486", pid)
	smm2Profils.Store(pid, SMM2Profil{PID: pid, Nom: "Juanjo", Pays: "FR"})
	defer smm2Profils.Delete(pid)

	if _, ok := profilPourIdentifiant(nsa, pid); !ok {
		t.Fatal("profil introuvable par NSA alors qu'il est enregistre sous son PID")
	}
	if _, ok := profilPourIdentifiant(pid, pid); !ok {
		t.Fatal("profil introuvable par son propre PID")
	}
	if _, ok := profilPourIdentifiant(999999, pid); ok {
		t.Fatal("un identifiant inconnu ne doit rien rendre")
	}
}

// Un catalogue vide est une reponse VALIDE, pas un echec : sur un serveur neuf,
// personne n'a publie. La forme doit quand meme etre exacte — liste puis booleen —
// sinon le client rejette tout.
func TestDataStoreSearchCoursesPostedByRendUnCatalogueVide(t *testing.T) {
	s := testSettings()
	ep := NewEndpoint(s)
	conn := NewConnection(ep, "88.1.2.3:12345", func([]byte) {})
	conn.PID = 1800001206

	req := NewRMCRequest(s, ProtocolDataStoreSMM2, MethodDataStoreSearchCoursesPostedBy, 74, nil)
	resp := DataStoreSMM2Handler()(conn, req)
	if resp.IsError {
		t.Fatalf("refus inattendu : %+v", resp)
	}
	in := NewStreamIn(resp.Body, s)
	if n := in.U32(); n != 0 {
		t.Fatalf("%d niveaux rendus, aucun n'a ete publie", n)
	}
	if in.Bool() {
		t.Fatal("le booleen final devrait etre faux")
	}
	if !in.EOF() {
		t.Fatal("octets en trop : le client exige eof()")
	}
}

// Un profil doit survivre a un redemarrage. Le test ecrit, vide la memoire, relit.
func TestSMM2ProfilsSurviventAuRedemarrage(t *testing.T) {
	dir := t.TempDir()
	ancien := smm2ProfilsFichier
	smm2ProfilsFichier = dir + "/profils.json"
	defer func() { smm2ProfilsFichier = ancien }()

	const pid = uint64(1800001206)
	smm2Profils.Store(pid, SMM2Profil{
		PID: pid, Nom: "Juanjo", Pays: "FR", Langue: 2,
		Mii: []byte{1, 2, 3}, Unk: [4]uint16{5, 6, 7, 8},
	})
	smm2SauverProfils()

	// on simule le redemarrage : la memoire disparait
	smm2Profils.Delete(pid)
	if _, ok := SMM2ProfilDe(pid); ok {
		t.Fatal("le profil aurait du disparaitre de la memoire")
	}

	SMM2ChargerProfils()
	p, ok := SMM2ProfilDe(pid)
	if !ok {
		t.Fatal("profil perdu apres redemarrage")
	}
	if p.Nom != "Juanjo" || p.Pays != "FR" || p.Langue != 2 {
		t.Fatalf("profil altere : %+v", p)
	}
	if string(p.Mii) != string([]byte{1, 2, 3}) {
		t.Fatalf("Mii altere : %x", p.Mii)
	}
	if p.Unk != [4]uint16{5, 6, 7, 8} {
		t.Fatalf("bloc u16 altere : %v", p.Unk)
	}
}

// Un fichier corrompu ne doit PAS effacer les profils en silence : mieux vaut un
// serveur qui se plaint qu'un serveur qui oublie tout le monde sans le dire.
func TestSMM2ProfilsCorrompusNeVidentPas(t *testing.T) {
	dir := t.TempDir()
	ancien := smm2ProfilsFichier
	smm2ProfilsFichier = dir + "/profils.json"
	defer func() { smm2ProfilsFichier = ancien }()

	if err := os.WriteFile(smm2ProfilsFichier, []byte("{ceci n'est pas du JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	const pid = uint64(1800009999)
	smm2Profils.Store(pid, SMM2Profil{PID: pid, Nom: "EnMemoire"})
	defer smm2Profils.Delete(pid)

	SMM2ChargerProfils() // ne doit pas paniquer ni purger

	if _, ok := SMM2ProfilDe(pid); !ok {
		t.Fatal("un fichier corrompu a efface un profil deja en memoire")
	}
	if _, err := os.Stat(smm2ProfilsFichier); err != nil {
		t.Fatal("le fichier corrompu a ete supprime : on perd la trace de l'incident")
	}
}
