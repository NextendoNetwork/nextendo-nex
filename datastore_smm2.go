package nex

// DataStoreSMM2 — protocole 0x73, la couche « contenu » de Super Mario Maker 2.
//
// CE QUE C'EST. Course World au complet passe par ici : publier un niveau, le
// parcourir, le noter, le commenter, consulter les classements. Kinnay en documente
// une centaine de methodes ; ce fichier n'en implemente PAS cent.
//
// POURQUOI SI PEU. On a deploye le serveur d'abord, sans une seule methode 0x73, et
// on a regarde ce que la console demandait vraiment. Elle a demande UNE chose, quatre
// fois de suite : la methode 48, GetUsers — c'est l'ecran « choisis un Mii ». Ecrire
// les cent methodes a l'aveugle aurait produit du code jamais appele et rate celles
// qui comptent. Le jeu nous dit lui-meme ce qu'il lui faut ; il suffit de l'ecouter.
//
// CE QUI N'EST PAS LA. Tout le reste de Course World. Les appels tomberont dans le
// journal avec leur numero de methode, et c'est ainsi qu'on saura quoi ecrire ensuite.

import (
	"fmt"
	"strconv"
	"sync"
)

// ProtocolDataStoreSMM2 est l'identifiant du protocole DataStore specifique a SMM2.
const ProtocolDataStoreSMM2 uint16 = 0x73

// Methodes. Seules celles qu'on traite sont nommees : baptiser les 90 autres donnerait
// l'illusion qu'elles existent.
const (
	// MethodDataStoreRegisterUser : creation du profil SMM2 — l'ecran ou le joueur
	// choisit son Mii et tape son nom. Requete : nom, un bloc de 4 u16, un qbuffer
	// (le Mii), langue, pays, identifiant d'appareil. REPONSE VIDE.
	MethodDataStoreRegisterUser uint32 = 47
	// MethodDataStoreGetUsers : la console reclame les profils d'une liste de PID.
	// Requete  : liste de PID + un u32 d'options.
	// Reponse  : liste de UserInfo + liste de resultats, un par PID demande.
	MethodDataStoreGetUsers uint32 = 48
	// MethodDataStoreSyncUserProfile : le jeu resynchronise SON profil.
	// Reponse : SyncUserProfileResult — pid, pseudo, les 4 u16, le Mii, pays, drapeaux.
	MethodDataStoreSyncUserProfile uint32 = 49
	// MethodDataStoreSearchCoursesPostedBy : « quels niveaux ce createur a-t-il
	// publies ? » — l'ecran « profil du createur ».
	// Reponse : liste de CourseInfo + un booleen.
	MethodDataStoreSearchCoursesPostedBy uint32 = 74
	// MethodDataStoreGetEventCourseStatus : etat des niveaux d'evenement. Sans corps.
	MethodDataStoreGetEventCourseStatus uint32 = 154
	// MethodDataStoreCanPostCourse : « ce joueur peut-il publier ? ». Sans parametres,
	// reponse Bool + Uint32. Documente par PretendoNetwork, pas par kinnay — dont la
	// liste saute de 59 a 65, ce qui m'a fait croire pendant des heures que ces methodes
	// n'etaient documentees nulle part.
	MethodDataStoreCanPostCourse uint32 = 60
	// MethodDataStoreGetMiiClothes : les vetements du Mii. Ce n'etait donc pas un
	// mystere, juste une page que je n'avais pas trouvee.
	MethodDataStoreGetMiiClothes uint32 = 63
)

// smm2Profils garde le profil de chaque joueur. En memoire pour l'instant : on veut
// d'abord voir le jeu accepter la creation. La persistance viendra quand on saura ce
// qu'il faut vraiment garder — et elle ira SUR DISQUE, jamais dans un JSON qu'on
// reserialise en entier : c'est ce schema-la qui a fait tomber le serveur le 12 aout.
var smm2Profils sync.Map // uint64 -> SMM2Profil

// SMM2Profil est ce que la console nous envoie a la creation du compte.
type SMM2Profil struct {
	PID      uint64
	Nom      string
	Mii      []byte // donnees du Mii, opaques : on les stocke, on ne les interprete pas
	Langue   uint8
	Pays     string
	Appareil string
	Unk      [4]uint16
}

// ResultDataStoreNotFound : l'objet demande n'existe pas. Valeur relevee dans
// errors.py de kinnay (0x00690004 = DataStore::NotFound), pas deduite.
const ResultDataStoreNotFound uint32 = 0x00690004

// DataStoreSMM2Handler traite le protocole 0x73.
func DataStoreSMM2Handler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodDataStoreRegisterUser:
			return handleDataStoreRegisterUser(conn, req)
		case MethodDataStoreGetUsers:
			return handleDataStoreGetUsers(conn, req)
		case MethodDataStoreSearchCoursesPostedBy:
			return handleDataStoreSearchCoursesPostedBy(conn, req)
		case MethodDataStoreSyncUserProfile:
			return handleDataStoreSyncUserProfile(conn, req)
		case MethodDataStoreCanPostCourse:
			return handleDataStoreCanPostCourse(conn, req)
		case MethodDataStoreGetEventCourseStatus, MethodDataStoreGetMiiClothes:
			// Reponse vide. Pour l'evenement, c'est VRAI : il n'y en a aucun en cours.
			return NewRMCSuccess(conn.Settings, ProtocolDataStoreSMM2, req.Method, req.CallID, nil)
		default:
			return notImplemented(conn, ProtocolDataStoreSMM2, req)
		}
	}
}

// handleDataStoreGetUsers repond « ce joueur n'a pas encore de profil ».
//
// C'est une reponse VRAIE, pas un contournement : sur un serveur neuf, personne n'a de
// profil SMM2. Le jeu est cense enchainer sur la creation — l'ecran du Mii, du nom et
// du pays — via RegisterUser. On repond donc une liste de profils VIDE et un
// DataStore::NotFound par PID demande.
//
// Ce qu'on ne peut pas encore faire : servir un profil EXISTANT. UserInfo compte 26
// champs et plusieurs structures imbriquees ; il faudra les ecrire quand on saura ce
// que le jeu accepte. Une chose a la fois, guidee par ce qu'il demande.
func handleDataStoreGetUsers(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)

	// La requete vient du CLIENT : on la lit sans lui faire confiance. Une liste
	// aberrante ne doit pas nous faire allouer une reponse aberrante.
	body := in
	if s.StructHeader {
		_ = in.U8()
		body = in.Substream()
	}
	pids := ReadList(body, func(i *StreamIn) uint64 { return i.PID() })
	option := body.U32()

	// TRADUCTION NSA -> PID. La console ne designe pas toujours un joueur par son PID
	// NEX : elle envoie parfois l'identifiant NSA de son compte Nintendo, un nombre de
	// sept a dix-neuf chiffres qui ne ressemble a aucun de nos PID. Cherche tel quel, il
	// ne correspond a rien, et nous repondions « ce joueur n'existe pas » a propos du
	// joueur qui posait la question.
	//
	// C'est ce qui bloquait la publication des super mondes : le jeu demandait ses
	// propres niveaux avec son NSA, recevait zero, et en concluait qu'ils avaient ete
	// effaces de Course World. Mesure le 2026-08-28 — le meme nombre figure dans le
	// journal d'authentification, ligne « NSA 11389664994428258486 -> account pid=... ».
	for i, id := range pids {
		pids[i] = SMM2PIDJoueur(id)
	}

	const maxPIDs = 256
	if len(pids) > maxPIDs {
		pids = pids[:maxPIDs]
	}

	// LISTE VIDE = « donne-moi MON profil ». C'est ce que fait la console : releve le
	// 2026-08-24, elle appelle GetUsers avec zero PID, systematiquement, y compris juste
	// apres avoir cree son profil. Repondre « zero profil demande, zero rendu » etait
	// litteralement exact et pratiquement inutile — le jeu ne voyait jamais son propre
	// compte et retombait sur un Mii generique.
	if len(pids) == 0 {
		pids = []uint64{conn.PID}
	}

	// On separe les PID en deux : ceux dont on a le profil, et les autres. Le client
	// attend les profils trouves dans la premiere liste, et un resultat par PID demande
	// dans la seconde — DANS L'ORDRE de la demande. Melanger les deux le desynchronise.
	type reponse struct {
		profil SMM2Profil
		trouve bool
	}
	rep := make([]reponse, 0, len(pids))
	trouves := 0
	for _, pid := range pids {
		if pr, ok := profilPourIdentifiant(pid, conn.PID); ok {
			rep = append(rep, reponse{pr, true})
			trouves++
		} else {
			rep = append(rep, reponse{})
		}
	}

	out := NewStreamOut(s)
	// users : uniquement les profils qu'on a.
	out.U32(uint32(trouves))
	for _, r := range rep {
		if r.trouve {
			ecrireUserInfo(out, r.profil)
		}
	}
	// results : un code par PID demande, dans le meme ordre.
	out.U32(uint32(len(rep)))
	for _, r := range rep {
		if r.trouve {
			out.Result(0) // succes
		} else {
			out.Result(ResultDataStoreNotFound)
		}
	}

	fmt.Printf("[DataStoreSMM2] GetUsers pid=%d option=%d demande %d profil(s) -> %d trouve(s)\n",
		conn.PID, option, len(pids), trouves)
	return NewRMCSuccess(s, ProtocolDataStoreSMM2, req.Method, req.CallID, out.Bytes())
}

// handleDataStoreRegisterUser enregistre le profil que le joueur vient de creer.
//
// La reponse est VIDE : le client de kinnay lit la reponse puis exige stream.eof(),
// donc le moindre octet en trop la ferait echouer. Tout le travail est du cote
// LECTURE — il faut consommer la requete exactement dans l'ordre ou elle est ecrite,
// sans quoi le champ suivant est lu au mauvais endroit.
//
// Ordre releve dans RegisterUserParam.load() de kinnay, pas devine :
//
//	name (string) · UnknownStruct1 (4 x u16) · Mii (qbuffer) · langue (u8) ·
//	pays (string) · identifiant d'appareil (string)
func handleDataStoreRegisterUser(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)

	// Les structures NEX portent une EN-TETE quand Settings.StructHeader est actif —
	// et il l'est sur Switch : un octet de version, puis la taille du contenu. Lire les
	// champs sans consommer cette en-tete decale tout, et c'est exactement l'erreur que
	// j'ai commise en premier : « unexpected EOF » sur 162 octets parfaitement valides.
	// La regle : une structure se lit TOUJOURS via son sous-flux.
	body := in
	if s.StructHeader {
		_ = in.U8() // version de RegisterUserParam
		body = in.Substream()
	}

	p := SMM2Profil{PID: conn.PID}
	p.Nom = body.String()

	// UnknownStruct1 est une structure a part entiere : elle a donc SA PROPRE en-tete,
	// imbriquee dans celle du parametre. Quatre u16 dont on ignore le sens ; on les
	// garde tels quels plutot que de leur inventer un nom.
	unk := body
	if s.StructHeader {
		_ = body.U8()
		unk = body.Substream()
	}
	for i := range p.Unk {
		p.Unk[i] = unk.U16()
	}

	p.Mii = body.QBuffer()
	p.Langue = body.U8()
	p.Pays = body.String()
	p.Appareil = body.String()

	// Si la lecture a derape, on ne garde RIEN : un profil a moitie lu vaut moins que
	// pas de profil, et il ferait mentir tout ce qu'on servira ensuite.
	if err := body.Err(); err != nil {
		fmt.Printf("[DataStoreSMM2] RegisterUser pid=%d : requete illisible (%v), %d octets\n",
			conn.PID, err, len(req.Body))
		return NewRMCError(s, ProtocolDataStoreSMM2, req.CallID, ResultCoreInvalidArgument)
	}

	smm2Profils.Store(conn.PID, p)
	smm2SauverProfils() // sur le disque : un redemarrage ne doit plus effacer le profil
	fmt.Printf("[DataStoreSMM2] RegisterUser pid=%d nom=%q pays=%q langue=%d mii=%d octets\n",
		conn.PID, p.Nom, p.Pays, p.Langue, len(p.Mii))

	return NewRMCSuccess(s, ProtocolDataStoreSMM2, req.Method, req.CallID, nil)
}

// SMM2ProfilDe rend le profil enregistre pour ce PID.
func SMM2ProfilDe(pid uint64) (SMM2Profil, bool) {
	v, ok := smm2Profils.Load(pid)
	if !ok {
		return SMM2Profil{}, false
	}
	return v.(SMM2Profil), true
}

// ecrireUserInfo serialise le profil d'un joueur au format UserInfo.
//
// Ordre releve dans UserInfo.save() de kinnay, champ par champ. Il n'y a pas de marge
// d'interpretation : un seul champ ecrit dans le mauvais ordre, ou d'une taille
// differente, decale tout le reste et le client rejette la reponse entiere.
//
// VERSION 0. La structure existe en versions 0 a 3 ; les versions superieures ajoutent
// des champs a la fin. On annonce 0 dans l'en-tete et on s'arrete la : le client lit la
// version qu'on declare et ne cherche pas ce qu'on n'a pas promis. On montera de
// version quand on aura de quoi remplir ces champs — pas avant, parce qu'annoncer une
// version qu'on ne sait pas remplir est exactement le genre de mensonge qui produit
// des bugs indebuggables.
//
// Les statistiques partent VIDES : elles sont vraies. Ce joueur n'a rien joue encore.
func ecrireUserInfo(out *StreamOut, p SMM2Profil) {
	s := out.Settings

	champs := NewStreamOut(s)
	champs.PID(p.PID)
	champs.String(codeMakerDe(p.PID))
	champs.String(p.Nom)

	// UnknownStruct1 : structure imbriquee, donc sa propre en-tete. On rend les quatre
	// u16 tels que la console nous les a donnes a l'inscription.
	unk1 := NewStreamOut(s)
	for _, v := range p.Unk {
		unk1.U16(v)
	}
	if s.StructHeader {
		champs.U8(0)
		champs.Buffer(unk1.Bytes())
	} else {
		champs.Write(unk1.Bytes())
	}

	champs.QBuffer(p.Mii) // le Mii, tel quel
	champs.String(p.Pays)
	champs.U8(0) // region
	// last_active : la date d'epoque EMPAQUETEE, jamais zero. Zero n'est pas une date
	// dans un champ de bits — c'est l'an 0, mois 0, jour 0.
	champs.DateTime(dateTimeEpoqueNex())
	// TROIS BOOLEENS INCONNUS, et l'un d'eux restreint probablement les commentaires.
	//
	// Le jeu dit « le CREATEUR du niveau les a restreints ». C'est donc un reglage du
	// createur, pas du niveau — il vit sur son profil, pas dans la fiche du cours. Ce
	// sont les seuls drapeaux que nous envoyions dans UserInfo, et ils valaient false.
	//
	// La documentation les donne « Unknown » et kinnay les nomme unk3 a unk5 sans plus.
	// On ne peut pas savoir lequel autrement qu'en essayant, d'ou un masque de bits
	// reglable sans redeploiement :
	//
	//   echo 7 > /opt/smm2/smm2_9998.forme   -> les trois a true  (defaut)
	//   echo 1 > ...                         -> seul unk3
	//   echo 2 > ...                         -> seul unk4
	//   echo 4 > ...                         -> seul unk5
	//   echo 0 > ...                         -> les trois a false (comportement precedent)
	// LES TROIS A true. Mesure le 2026-08-24 : a false, le jeu affichait « le createur a
	// restreint les commentaires ». Le message parlait du CREATEUR, donc du profil et
	// non de la fiche du niveau — c'est ce qui a mene ici.
	mBool := SMM2MasqueBooleens()
	champs.Bool(mBool&1 != 0) // unk3
	champs.Bool(mBool&2 != 0) // unk4
	champs.Bool(mBool&4 != 0) // unk5

	// Le SENS de ces tables vient de TheGreatRambler/MariOver, un client qui lit les
	// vrais serveurs de Nintendo. Kinnay donne leur FORME (map<u8,u32>) mais pas ce
	// qu'elles contiennent ; MariOver nomme chaque case. Les envoyer vides n'etait donc
	// pas neutre : le jeu ne trouvait aucun compteur et retombait sur un etat par
	// defaut ou le quota de publication paraissait epuise — le fameux « 32/32 » affiche
	// alors que le joueur n'a rien publie.
	stats := func(vals ...uint32) {
		m := make(map[uint8]uint32, len(vals))
		for i, v := range vals {
			m[uint8(i)] = v
		}
		WriteMap(champs, m, func(o *StreamOut, k uint8) { o.U8(k) }, func(o *StreamOut, v uint32) { o.U32(v) })
	}

	// Les CLES de ces tables viennent de la documentation PretendoNetwork, l'ORDRE des
	// tables de la bibliotheque de kinnay (datastore_smm2.py, UserInfo.save). Deux
	// erreurs de ce fichier apparaissent en les confrontant, corrigees ici :
	// « reussites » et « tentatives » etaient inversees dans play_stats, et
	// multiplayer_stats emploie des cles NON CONTIGUES (0, 2, 3, 10, 11) alors qu'on en
	// ecrivait quinze a la suite — les cles 4 a 9 et 12 a 14 n'existent pas.
	st := SMM2StatsDe(p.PID)

	// play_stats : 0 parties, 1 reussites, 2 tentatives, 3 morts.
	//
	// Les morts restent a zero : personne ne nous les envoie. Le client rapporte le
	// resultat d'une partie (methode 96) mais pas le nombre de fois qu'on y est mort,
	// et l'inventer serait afficher un chiffre faux a cote de trois vrais.
	stats(st.Parties, st.Reussites, st.Tentatives, 0)

	// maker_stats : 0 « j'aime » recus, 1 points de createur.
	//
	// A zero, et c'est exact : Nextendo n'a pas encore de systeme de notes, donc
	// personne n'a recu de coeur. Les points de createur en decoulent chez Nintendo.
	stats(0, 0)

	// endless_challenge_high_scores : facile, normal, expert, super expert.
	// A zero parce que le mode sans fin ne demarre pas encore.
	stats(0, 0, 0, 0)

	// multiplayer_stats : cles 0 score, 2 parties versus, 3 victoires versus,
	// 10 parties coop, 11 victoires coop. Vide tant que le multijoueur ne demarre pas :
	// une table de zeros affirmerait « zero victoire sur zero partie », ce qui est vrai
	// mais qu'on ne mesure pas.
	WriteMap(champs, map[uint8]uint32{}, func(o *StreamOut, k uint8) { o.U8(k) }, func(o *StreamOut, v uint32) { o.U32(v) })

	// unk7 : points de createur de la semaine.
	stats(0)
	WriteList(champs, []struct{}{}, func(o *StreamOut, _ struct{}) {}) // badges
	// unk8 : premieres reussites, records du monde. Les deux se deduisent du catalogue :
	// on sait qui a fini chaque niveau le premier et qui en detient le meilleur temps.
	stats(st.PremieresReussites, st.Records)
	// unk9 : niveaux PUBLIES, et maximum autorise. C'est CE champ qui pilote l'ecran
	// « 32/32 » : premiere case le nombre deja publie, seconde le plafond.
	//
	// Le zero etait vrai le jour ou personne n'avait rien publie, et le commentaire plus
	// bas le disait honnetement. Il a cesse d'etre vrai des la premiere publication : le
	// compteur du joueur restait a zero alors que son niveau existait. Une constante qui
	// dit la verite « pour le moment » est une constante qui mentira, et il faut lui
	// donner une date de peremption plutot qu'une note d'intention.
	stats(SMM2NombrePublies(p.PID), smm2MaxPublications)

	// --- LES SEPT CHAMPS DE LA REVISION 3 -------------------------------------------
	//
	// Nous nous arretions ici et annoncions la VERSION 0. Le jeu attend la 3 :
	// ocw-server declare `DataStructure uint8 \`revision:"3"\`` sur UserInfo. Une
	// revision trop basse fait cesser la lecture la ou nous cessons d'ecrire, et tout ce
	// qui suit n'existe simplement pas pour le client.
	//
	// Les noms viennent du meme fichier, et ils confirment au passage ce qu'on avait
	// trouve a la sonde cette nuit : les trois booleens d'avant s'appellent Following,
	// CommentsEnabled et TagsLocked.
	champs.Bool(false)                   // IsNintendoEmployee — non, evidemment
	champs.DateTime(dateTimeEpoqueNex()) // LastUploadedLevel
	champs.Bool(false)                   // Unk12

	// Unk13 : structure imbriquee { Bool, DateTime }.
	u13 := NewStreamOut(s)
	u13.Bool(false)
	u13.DateTime(dateTimeEpoqueNex())
	if s.StructHeader {
		champs.U8(0)
		champs.Buffer(u13.Bytes())
	} else {
		champs.Write(u13.Bytes())
	}

	// SuperWorldId : l'identifiant du super monde de ce createur, ou la chaine vide s'il
	// n'en a pas. C'est CE champ qui fait apparaitre son monde sur son profil.
	//
	// Il valait la chaine vide en dur, avec la note « personne n'a de super monde chez
	// nous » : vrai le jour ou c'etait ecrit, faux depuis que les methodes 159 a 166
	// existent. Le jeu fournit le compteur, comme pour les publications et les stats.
	//
	// Longueur ZERO quand il est vide, PAS longueur 1 + octet nul : ce champ est vide pour
	// la grande majorite des joueurs, et l'octet de trop decalerait tout ce qui suit.
	champs.StringVideZero(SMM2SuperMondeID(p.PID))
	// Unk15 : une table dont la cle 0 compte les super mondes JOUES par ce joueur.
	// La forme est la bonne ; le compte reste a zero tant qu'on ne l'agrege pas.
	stats(0)

	// Unk16 : VRAI. Nous ecrivions faux.
	//
	// Personne ne sait ce que ce booleen commande — ocw-server le note « this value
	// changes constantly, likely important » et n'en dit pas plus. Mais leur serveur
	// publie les super mondes et envoie VRAI, le notre les refusait et envoyait FAUX.
	// Entre deux valeurs inconnues, on prend celle qui accompagne un comportement qui
	// marche, et on ecrit ici que c'est une mesure et non une deduction.
	champs.Bool(true)

	if s.StructHeader {
		out.U8(3) // revision 3, comme l'attend le jeu
		out.Buffer(champs.Bytes())
		return
	}
	out.Write(champs.Bytes())
}

// codeMakerDe fabrique le code Maker affiche a cote du profil. Il doit etre STABLE
// pour un joueur donne — c'est une identite visible, pas un identifiant technique :
// il apparait a l'ecran, les joueurs se le communiquent, et le voir changer d'une
// session a l'autre serait pire que ne pas en avoir.
func codeMakerDe(pid uint64) string {
	const alphabet = "0123456789BCDFGHJKLMNPQRSTVWXY"
	code := make([]byte, 9)
	v := pid
	for i := range code {
		code[i] = alphabet[v%uint64(len(alphabet))]
		v /= uint64(len(alphabet))
	}
	return string(code)
}

// profilPourIdentifiant retrouve un profil a partir de l'identifiant que le JEU emploie.
//
// Super Mario Maker 2 ne demande pas les profils par notre PID interne : il les demande
// par l'identifiant NSA de la console, celui qui sert a se connecter. Releve sur les
// octets bruts d'une vraie requete le 2026-08-24 : la console reclamait
// 11389664994428258486, qui est le NSA que l'authentification avait justement traduit
// en PID 1800001206 quelques lignes plus haut dans le meme journal.
//
// On essaie donc, dans l'ordre :
//  1. l'identifiant tel quel, au cas ou ce SERAIT deja un PID ;
//  2. sa traduction NSA -> PID, apprise a l'authentification ;
//  3. le PID de l'appelant, quand il se reclame lui-meme.
//
// La lecon vaut au-dela de SMM2 : « pid » dans une structure documentee ne garantit pas
// que la valeur soit NOTRE pid. Ici les deux existaient et ne coincidaient pas.
func profilPourIdentifiant(demande, appelant uint64) (SMM2Profil, bool) {
	if p, ok := SMM2ProfilDe(demande); ok {
		return p, true
	}
	if pid, ok := PIDForLoginName(strconv.FormatUint(demande, 10)); ok {
		if p, ok := SMM2ProfilDe(pid); ok {
			return p, true
		}
	}
	if demande == appelant {
		return SMM2Profil{}, false
	}
	return SMM2Profil{}, false
}

// handleDataStoreSearchCoursesPostedBy rend les niveaux publies par un createur.
//
// La liste est VIDE, et c'est la verite : personne n'a encore publie quoi que ce soit
// sur ce serveur. Ce n'est pas un bouchon — le jour ou des niveaux existeront, c'est
// ici qu'ils sortiront, et la forme de la reponse ne changera pas.
//
// Reponse relevee chez kinnay : liste de CourseInfo, puis un booleen. On n'a donc PAS
// besoin d'implementer CourseInfo pour repondre correctement a un catalogue vide —
// raison de plus pour ne pas l'ecrire a l'aveugle.
func handleDataStoreSearchCoursesPostedBy(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	out := NewStreamOut(s)
	WriteList(out, []struct{}{}, func(o *StreamOut, _ struct{}) {}) // courses
	out.Bool(false)                                                 // result

	fmt.Printf("[DataStoreSMM2] SearchCoursesPostedBy pid=%d -> aucun niveau publie\n", conn.PID)
	return NewRMCSuccess(s, ProtocolDataStoreSMM2, req.Method, req.CallID, out.Bytes())
}

// handleDataStoreCanPostCourse repond a « ce joueur peut-il publier un niveau ? ».
//
// Structure documentee par PretendoNetwork (docs/nex/protocols/datastore/
// super-mario-maker-2.md) : Bool puis Uint32. Les deux champs y sont marques
// « Unknown », mais l'ORDRE et les TYPES sont fermes — c'est ce qui manquait.
//
// Ce qui a coute la nuit : j'ai d'abord repondu un Uint32 seul, en supposant un quota,
// parce que je n'avais pas trouve cette page. Le jeu lisait le premier octet du nombre
// comme le booleen et le reste comme un entier tronque. Il ne s'en plaignait pas : il
// renoncait poliment, sans jamais demander le nom du niveau.
//
// Le booleen est l'autorisation. On le met a vrai : sur ce serveur, rien n'interdit de
// publier. Le Uint32 reste inconnu meme en amont ; zero est la valeur neutre.
func handleDataStoreCanPostCourse(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	out := NewStreamOut(s)
	out.Bool(true) // autorise
	out.U32(0)     // champ non documente, meme chez Pretendo
	fmt.Printf("[DataStoreSMM2] CanPostCourse pid=%d -> autorise\n", conn.PID)
	return NewRMCSuccess(s, ProtocolDataStoreSMM2, req.Method, req.CallID, out.Bytes())
}

// smm2QuotaPublication : places de publication RESTANTES annoncees au joueur.
//
// 32, et pas un nombre invente plus grand : c'est le maximum que SMM2 affiche lui-meme
// (« 32/32 » sur l'ecran de sauvegarde). Annoncer davantage laisserait le jeu proposer
// des emplacements qu'il n'est pas cense avoir, et on ne sait pas comment il reagit a
// une valeur hors de son echelle. Rester dans les bornes du jeu.
//
// Quand la publication marchera, cette valeur devra devenir DYNAMIQUE : 32 moins le
// nombre de niveaux reellement publies par ce joueur. Tant qu'il n'y en a aucun, la
// constante dit la verite.
const smm2QuotaPublication uint32 = 32

// smm2MaxPublications : plafond de niveaux publiables annonce dans UserInfo.unk9[1].
// Le champ unk9 est nomme par MariOver : [0] = deja publies, [1] = maximum.
const smm2MaxPublications uint32 = 32

// SMM2CompteurPublies : combien de niveaux ce joueur a publies.
//
// Le catalogue des niveaux vit dans le serveur de jeu, pas dans cette bibliotheque, qui
// ne connait que les profils. Plutot que d'y faire remonter le catalogue — ce qui
// inverserait la dependance — le serveur depose ici sa fonction de comptage au
// demarrage. Tant que personne ne la fournit, on rend zero, ce qui est le comportement
// d'avant et ne casse aucun autre jeu.
// SMM2SuperMondeIDFn : fourni par le jeu, rend l'identifiant du super monde d'un createur.
// Absent (ou rendant la chaine vide) = ce joueur n'en a pas, ce qui est le cas normal.
// SMM2PIDJoueurFn : fourni par le jeu, traduit un identifiant de joueur venu du client
// vers un PID NEX. La bibliotheque ne peut pas le faire elle-meme — la correspondance
// NSA -> compte vit dans le service de comptes, que seul le jeu sait joindre.
var SMM2PIDJoueurFn func(id uint64) uint64

// SMM2PIDJoueur traduit s'il y a de quoi, et rend l'identifiant inchange sinon. Un
// traducteur absent doit laisser passer, pas tout casser.
func SMM2PIDJoueur(id uint64) uint64 {
	if SMM2PIDJoueurFn == nil {
		return id
	}
	return SMM2PIDJoueurFn(id)
}

var SMM2SuperMondeIDFn func(pid uint64) string

// SMM2SuperMondeID interroge le fournisseur s'il a ete pose.
func SMM2SuperMondeID(pid uint64) string {
	if SMM2SuperMondeIDFn == nil {
		return ""
	}
	return SMM2SuperMondeIDFn(pid)
}

var SMM2CompteurPublies func(pid uint64) uint32

// SMM2NombrePublies interroge le compteur s'il a ete fourni.
func SMM2NombrePublies(pid uint64) uint32 {
	if SMM2CompteurPublies == nil {
		return 0
	}
	return SMM2CompteurPublies(pid)
}

// handleDataStoreSyncUserProfile rend le profil du joueur connecte.
//
// Structure relevee dans SyncUserProfileResult.save() de kinnay :
//
//	pid (u64) · pseudo (string) · UnknownStruct1 (4 x u16) · Mii (qbuffer) ·
//	u8 · pays (string) · u8 · bool · bool
//
// A noter, parce que ca a coute une hypothese : cette reponse ne contient AUCUN
// compteur de niveaux publies. Le « 32/32 » affiche par le jeu ne vient donc ni d'ici
// ni de la methode 60 — les deux ont ete verifiees. Il reste a trouver.
func handleDataStoreSyncUserProfile(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	p, ok := SMM2ProfilDe(conn.PID)
	if !ok {
		// Pas de profil : on le dit franchement plutot que d'en inventer un.
		return NewRMCError(s, ProtocolDataStoreSMM2, req.CallID, ResultDataStoreNotFound)
	}

	champs := NewStreamOut(s)
	champs.U64(p.PID)
	champs.String(p.Nom)
	unk1 := NewStreamOut(s)
	for _, v := range p.Unk {
		unk1.U16(v)
	}
	if s.StructHeader {
		champs.U8(0)
		champs.Buffer(unk1.Bytes())
	} else {
		champs.Write(unk1.Bytes())
	}
	champs.QBuffer(p.Mii)
	champs.U8(p.Langue)
	champs.String(p.Pays)
	champs.U8(0)
	champs.Bool(false)
	champs.Bool(false)

	out := NewStreamOut(s)
	if s.StructHeader {
		out.U8(0)
		out.Buffer(champs.Bytes())
	} else {
		out.Write(champs.Bytes())
	}

	fmt.Printf("[DataStoreSMM2] SyncUserProfile pid=%d nom=%q\n", conn.PID, p.Nom)
	return NewRMCSuccess(s, ProtocolDataStoreSMM2, req.Method, req.CallID, out.Bytes())
}

// EcrireUserInfo expose l'ecriture d'un UserInfo aux paquets qui en ont besoin hors de
// cette bibliotheque — la methode 131 (GetUserOrCourse) rend un UserInfo et un
// CourseInfo dans la meme reponse, et le CourseInfo est construit ailleurs.
func EcrireUserInfo(out *StreamOut, p SMM2Profil) { ecrireUserInfo(out, p) }

// SMM2StatsJoueur : ce qu'on sait de l'activite d'un joueur, pour remplir UserInfo.
type SMM2StatsJoueur struct {
	Parties            uint32 // parties jouees
	Reussites          uint32 // niveaux termines
	Tentatives         uint32 // essais cumules
	PremieresReussites uint32 // niveaux dont il est le premier a etre venu a bout
	Records            uint32 // niveaux dont il detient le meilleur temps
}

// SMM2CompteurStats : fourni par le serveur de jeu, qui tient le catalogue et les
// resultats de parties. Meme raison que SMM2CompteurPublies : cette bibliotheque ne
// connait que les profils, et lui faire remonter le catalogue inverserait la dependance.
var SMM2CompteurStats func(pid uint64) SMM2StatsJoueur

// SMM2StatsDe interroge le compteur s'il existe. Sinon des zeros, ce qui est le
// comportement d'avant et ne change rien pour les autres jeux.
func SMM2StatsDe(pid uint64) SMM2StatsJoueur {
	if SMM2CompteurStats == nil {
		return SMM2StatsJoueur{}
	}
	return SMM2CompteurStats(pid)
}

// SMM2MasqueBooleens : les trois booleens inconnus d'UserInfo, regles depuis le serveur.
// Vaut 0 tant que personne ne fournit la fonction, ce qui preserve le comportement des
// autres jeux.
var SMM2MasqueBooleensFn func() uint32

func SMM2MasqueBooleens() uint32 {
	if SMM2MasqueBooleensFn == nil {
		return 0
	}
	return SMM2MasqueBooleensFn()
}

// SMM2PseudoDe et SMM2MiiDe exposent le pseudo et le Mii d'un joueur aux paquets qui en
// ont besoin hors de cette bibliotheque — les commentaires portent l'un et l'autre.
func SMM2PseudoDe(pid uint64) string {
	if p, ok := SMM2ProfilDe(pid); ok {
		return p.Nom
	}
	return ""
}

func SMM2MiiDe(pid uint64) []byte {
	if p, ok := SMM2ProfilDe(pid); ok {
		return p.Mii
	}
	return nil
}

// dateTimeEpoqueNex : le 1er janvier 1970 au format DateTime. Zero n'est pas une date
// valide dans ce format — c'est un champ de bits, et zero y designe l'an 0.
func dateTimeEpoqueNex() uint64 {
	return uint64(MakeDateTime(1970, 1, 1, 0, 0, 0))
}
