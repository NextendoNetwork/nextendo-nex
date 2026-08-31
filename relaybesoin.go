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
			if maintenant.Sub(v.dernier) > besoinFenetre {
				delete(besoins, k)
			}
		}
		besoinNet = maintenant
	}

	if reussi {
		delete(besoins, pid)

		return
	}

	e := besoins[pid]
	if e == nil || maintenant.Sub(e.dernier) > besoinFenetre {
		e = &besoinEtat{}
		besoins[pid] = e
	}
	e.echecs++
	e.dernier = maintenant

	if e.echecs == besoinSeuil {
		fmt.Printf("[relais-besoin] pid=%d : %d echecs de percage — relais autorise pour lui\n", pid, e.echecs)
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
