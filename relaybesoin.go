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
// paie donc son premier echec en direct, puis passe par le relais — ce qui est exactement le
// bon compromis : personne ne perd un chemin direct qui fonctionne.

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
)

type besoinEtat struct {
	echecs  int
	dernier time.Time

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

// NotePercage enregistre le verdict d une console sur son percage direct. Un succes efface
// l ardoise : le joueur vient de prouver qu il n a pas besoin du relais.
func NotePercage(pid uint64, reussi bool) {
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
			// qu on veut suivre.
			if v.reussites > 0 {
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
		fond.dernier = maintenant

		return
	}

	if maintenant.Sub(fond.dernier) > besoinFenetre {
		fond.echecs = 0
	}
	fond.echecs++
	fond.dernier = maintenant

	if fond.echecs == besoinSeuil {
		fmt.Printf("[relais-besoin] pid=%d : %d echecs de percage — relais autorise pour lui\n", pid, fond.echecs)
	}
}

// BesoinDeRelais dit si ce joueur a echoue assez recemment pour justifier le detour.
func BesoinDeRelais(pid uint64) bool {
	besoinMu.Lock()
	defer besoinMu.Unlock()

	e := besoins[pid]

	return e != nil && e.echecs >= besoinSeuil && time.Since(e.dernier) <= besoinFenetre
}

// BesoinStats rend le nombre de joueurs actuellement eligibles, pour le tableau de bord.
func BesoinStats() int {
	besoinMu.Lock()
	defer besoinMu.Unlock()

	n := 0
	for _, e := range besoins {
		if e.echecs >= besoinSeuil && time.Since(e.dernier) <= besoinFenetre {
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
	for pid, e := range besoins {
		if e.echecs >= besoinSeuil && time.Since(e.dernier) <= besoinFenetre {
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
