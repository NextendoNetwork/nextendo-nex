package nex

// Qui a BESOIN du relais.
//
// Arme pour tout le monde, le relais fait passer par la France deux joueurs qui se
// joignaient tres bien en direct, et le moindre defaut du relais casse une partie qui
// marchait. Mesure du 2026-08-31 : arme globalement, il a mis dehors des joueurs qui
// n avaient aucun probleme — huit se sont manifestes en une minute.
//
// Le tri ne peut pas se faire sur le type de NAT : sur la meme production, 945 stations
// annoncent natm=1, 239 natm=2, et AUCUNE natm=3. Le NAT declare ne distingue pas les 23%
// qui echouent.
//
// Ce qui les distingue, c est qu ils ont deja echoue. ReportNATTraversalResult est le seul
// endroit ou une console dit si le percage a marche ; on s en sert comme critere. Un joueur
// paie donc ses premiers echecs en direct (deux en general, un seul en NAT strict, voir
// besoinSeuilStrict), puis passe par le relais : personne ne perd un chemin direct qui
// fonctionne.

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	// besoinFenetre : au-dela, un echec ne dit plus rien de l etat actuel du reseau du
	// joueur (il a pu brancher un cable, activer l UPnP, changer de reseau).
	besoinFenetre = 15 * time.Minute
	// besoinSeuil : deux echecs, pas un. Un echec isole arrive a tout le monde — pair parti,
	// paquet perdu — et ne justifie pas de detourner tout son trafic.
	besoinSeuil = 2
	// besoinSeuilStrict : UN echec suffit quand il est de classe Pia 0515 ou 0516 (classePia,
	// nattraversal.go, table du jeu 0xbd170) : un cote en natm 1, l autre en natm 2 (mappage
	// dependant de la destination, NAT strict). La, l echec n est pas de la malchance, et
	// attendre le deuxieme fait payer au joueur une tentative de plus pour rien. Toutes les
	// autres classes gardent le seuil de deux : 0513 (natm 1 des deux cotes), 0514 (natm 2
	// des deux cotes), 0583-0586 (meme adresse publique) et « ? » (pair deja parti ou natm
	// inconnu).
	//
	// Rejeu des verdicts de prod (un outil interne,
	// regle « 0515/0516 seulement, classe connue » ; fenetre 15 min, succes direct = ardoise
	// effacee, verdicts relayes non comptes, etat vide a chaque redemarrage ; relais compte a
	// 83 % de reussite, mesure depuis l ouverture du 16/10 21:21Z) :
	//   - rejeu par defaut (sortie_politiques_strict.txt, reproductible : 4 598 verdicts du
	//     15/10 21:59Z au 16/10 22:35Z) : echecs des percages directs 0513 10 % (390/3988),
	//     0515 35 % (56/161), 0516 57 % (93/162), 0514 3 sur 3 ; seuil 1 en 0515/0516 : +31
	//     echecs directs relayes pour +64 reussites directes detournees, environ +15 percages
	//     reussis nets, taux d echec attendu 11,0 % -> 10,7 % ;
	//   - rejeu etendu au 17/10 00:08Z (sortie_politiques_strict_jusqu_0008.txt, 5 368
	//     verdicts, 123 relayes depuis l ouverture) : +44 echecs relayes pour +82 detours,
	//     environ +22 nets, 11,5 % -> 11,1 % ; depuis l ouverture (1 259 verdicts en 2,8 h) :
	//     relais sur 13,8 % des verdicts au lieu de 9,8 %, echecs attendus 13,4 % -> 12,4 %,
	//     13 des 61 echecs jamais rattrapes couverts au lieu de 7.
	// Ajouter 0514 n apporterait que +2 echecs relayes (3 verdicts sur la periode).
	besoinSeuilStrict = 1
)

// classeNATStrict : les classes Pia (classePia) ou un seul echec suffit (besoinSeuilStrict).
func classeNATStrict(classe string) bool {
	switch classe {
	case "0515", "0516":
		return true
	}

	return false
}

type besoinEtat struct {
	echecs  int
	dernier time.Time
	// strict : un des echecs de la fenetre en cours etait de classe NAT strict
	// (classeNATStrict). Efface avec l ardoise : succes direct ou fenetre expiree.
	strict bool

	// Historique de fond, sans fenetre : combien de percages DIRECTS ce joueur a tentes et
	// combien ont abouti. Sert a reperer ceux qui ne se connectent JAMAIS — 45 sur 113 le
	// 2026-08-31 — a qui le relais ne peut rien enlever puisqu ils ne jouent pas.
	//
	// Ces compteurs ne bougent que sur des verdicts directs : une fois le joueur relaye,
	// relayvu.go coupe l alimentation et ils se figent, au lieu de se confirmer eux-memes.
	essais    int
	reussites int
}

var (
	besoinMu  sync.Mutex
	besoins   = map[uint64]*besoinEtat{}
	besoinNet time.Time
)

// NotePercage enregistre le verdict d une console sur son percage direct, classe inconnue :
// seuil de deux echecs. Garde pour les appelants qui n ont pas la classe Pia.
func NotePercage(pid uint64, reussi bool) {
	NotePercageStrict(pid, reussi, false)
}

// NotePercageClasse enregistre le verdict avec la classe Pia du percage (classePia) : un
// echec de classe NAT strict (0515, 0516) rend le joueur eligible des le premier.
func NotePercageClasse(pid uint64, reussi bool, classe string) {
	NotePercageStrict(pid, reussi, classeNATStrict(classe))
}

// NotePercageStrict enregistre le verdict d une console sur son percage direct. Un succes
// efface l ardoise : le joueur vient de prouver qu il n a pas besoin du relais. strict dit
// que l echec est de classe NAT strict ; ignore pour un succes.
func NotePercageStrict(pid uint64, reussi bool, strict bool) {
	if pid == 0 {
		return
	}

	besoinMu.Lock()
	defer besoinMu.Unlock()

	maintenant := time.Now()

	// Menage, au plus une fois par minute : sans lui la carte grandit avec chaque joueur
	// jamais revu.
	if maintenant.Sub(besoinNet) > time.Minute {
		for k, v := range besoins {
			if maintenant.Sub(v.dernier) <= besoinFenetre {
				continue
			}
			// On oublie ceux qui se connectent : il n y a rien a retenir d eux. On GARDE
			// ceux qui n ont jamais reussi, sinon CandidatsRelais perdrait la memoire
			// toutes les quinze minutes et personne ne serait jamais repere. La carte
			// reste donc bornee par la population en panne, qui est justement celle
			// qu on veut suivre. Une entree sans un seul essai (pair mis en cause par
			// NoteCauseStricte, jamais rapporteur) n a rien a garder non plus.
			if v.reussites > 0 || v.essais == 0 {
				delete(besoins, k)
			}
		}
		besoinNet = maintenant
	}

	// L historique de fond survit a l effacement de l ardoise, et se met a jour dans les deux
	// cas — c est le seul endroit ou l on voit un joueur reussir.
	fond := besoins[pid]
	if fond == nil {
		fond = &besoinEtat{}
		besoins[pid] = fond
	}
	fond.essais++
	if reussi {
		fond.reussites++
		// Ardoise remise a zero : il vient de prouver qu il n a pas besoin du relais.
		fond.echecs = 0
		fond.strict = false
		fond.dernier = maintenant

		return
	}

	if maintenant.Sub(fond.dernier) > besoinFenetre {
		fond.echecs = 0
		fond.strict = false
	}
	avant := fond.eligible(maintenant)
	fond.echecs++
	fond.strict = fond.strict || strict
	fond.dernier = maintenant

	if !avant && fond.eligible(maintenant) {
		if fond.strict {
			fmt.Printf("[relais-besoin] pid=%d : %d echec(s) de percage dont un en NAT strict, relais autorise pour lui\n", pid, fond.echecs)
		} else {
			fmt.Printf("[relais-besoin] pid=%d : %d echecs de percage, relais autorise pour lui\n", pid, fond.echecs)
		}
	}
}

// NoteCauseStricte compte un echec de NAT strict au PAIR d un percage direct rate en classe
// 0515 : le rapporteur est en natm 1, ce pair en natm 2 (classePia), c est son NAT qui fait
// echouer la paire. Dans Smash, seul l arrivant rapporte : prod du 17/10 00:30Z a 03:40Z,
// aucun des 53 echecs directs 0515/0516 n a de verdict miroir du pair dans les 60 s, et les
// 0515 visent 5 pairs natm 2 seulement, qui font echouer jusqu a 18 percages chacun. Sans
// cette note, le pair n etait jamais eligible et chaque nouvel arrivant payait son propre
// echec direct contre lui.
//
// Ni essais ni reussites ne bougent : ce n est pas un verdict de ce joueur sur son percage
// (CandidatsRelais n en tient pas compte). La fenetre et l ardoise sont celles de
// NotePercageStrict : un succes direct du pair l efface.
func NoteCauseStricte(pid, rapporteur uint64) {
	if pid == 0 {
		return
	}

	besoinMu.Lock()
	defer besoinMu.Unlock()

	maintenant := time.Now()
	e := besoins[pid]
	if e == nil {
		e = &besoinEtat{}
		besoins[pid] = e
	}
	if maintenant.Sub(e.dernier) > besoinFenetre {
		e.echecs = 0
		e.strict = false
	}
	avant := e.eligible(maintenant)
	e.echecs++
	e.strict = true
	e.dernier = maintenant

	if !avant && e.eligible(maintenant) {
		fmt.Printf("[relais-besoin] pid=%d : pair natm 2 d un echec 0515 rapporte par pid=%d, relais autorise pour lui\n", pid, rapporteur)
	}
}

// ProlongerBesoin repousse l echeance d un joueur eligible en NAT strict, sans rien compter,
// quand il vient de rendre un verdict RELAYE de classe 0516 (lui en natm 2, le pair en natm
// 1). Sans elle, l eligibilite expirait 15 min apres le dernier echec direct alors que le
// joueur jouait par le relais : il repayait un echec direct par quart d heure pour la
// retrouver. Prod du 17/10 00:30Z a 03:40Z : 12 re-eligibilites apres expiration avec des
// verdicts relayes entre les deux, dont 22 de classe 0516.
//
// Pas d auto-entretien au sens de relayvu.go : ni echecs ni essais ne bougent, et la
// condition porte sur le natm que la console annonce encore (natm 2), une propriete de son
// NAT, pas sur le verdict relaye. Si son reseau change (natm 1), ses verdicts relayes ne
// sont plus 0516, l echeance n est plus repoussee et il est de nouveau mesure en direct
// 15 min plus tard.
func ProlongerBesoin(pid uint64) {
	if pid == 0 {
		return
	}

	besoinMu.Lock()
	defer besoinMu.Unlock()

	maintenant := time.Now()
	if e := besoins[pid]; e.eligible(maintenant) && e.strict {
		e.dernier = maintenant
	}
}

// eligible : assez d echecs directs dans la fenetre, un seul s il etait en NAT strict.
// Verrou besoinMu tenu par l appelant.
func (e *besoinEtat) eligible(maintenant time.Time) bool {
	if e == nil || maintenant.Sub(e.dernier) > besoinFenetre {
		return false
	}
	if e.strict {
		return e.echecs >= besoinSeuilStrict
	}

	return e.echecs >= besoinSeuil
}

// BesoinDeRelais dit si ce joueur a echoue assez recemment pour justifier le detour.
func BesoinDeRelais(pid uint64) bool {
	besoinMu.Lock()
	defer besoinMu.Unlock()

	return besoins[pid].eligible(time.Now())
}

// BesoinStats rend le nombre de joueurs actuellement eligibles, pour le tableau de bord.
func BesoinStats() int {
	besoinMu.Lock()
	defer besoinMu.Unlock()

	n := 0
	maintenant := time.Now()
	for _, e := range besoins {
		if e.eligible(maintenant) {
			n++
		}
	}

	return n
}

// BesoinListe rend les PID actuellement eligibles, tries.
//
// Le NOMBRE d eligibles a suffi a reperer que le critere se nourrissait lui-meme (14 sur 46,
// puis 134 sur 151 une heure et demie plus tard) ; la LISTE dit LESQUELS, donc si la
// croissance vient de joueurs reellement en panne ou de la boucle.
func BesoinListe() []uint64 {
	besoinMu.Lock()
	defer besoinMu.Unlock()

	out := make([]uint64, 0, len(besoins))
	maintenant := time.Now()
	for pid, e := range besoins {
		if e.eligible(maintenant) {
			out = append(out, pid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

// jamaisConnecteSeuil : au-dela, un joueur sans une seule reussite n a pas « eu de malchance »,
// il n a pas de chemin direct du tout. Volontairement haut : ces joueurs sont ajoutes
// automatiquement a la liste du relais, et le critere doit etre incontestable.
const jamaisConnecteSeuil = 10

// CandidatsRelais rend les joueurs qui n ont JAMAIS reussi un percage direct en au moins
// jamaisConnecteSeuil tentatives.
//
// Le relais ne peut rien leur enlever : ils ne jouent pas. C est ce qui rend leur ajout
// automatique defendable, la ou ouvrir le relais a tout le monde ne l est pas.
func CandidatsRelais() []uint64 {
	besoinMu.Lock()
	defer besoinMu.Unlock()

	out := make([]uint64, 0, 8)
	for pid, e := range besoins {
		if e.essais >= jamaisConnecteSeuil && e.reussites == 0 {
			out = append(out, pid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}
