package nex

// Quelles paires sont passees par le relais.
//
// ReportNATTraversalResult est le verdict d une console sur son percage DIRECT. Mais des que
// le candidat public a ete repointe vers le relais, il n y a plus de chemin direct : le
// verdict est FAILED par construction. L eligibilite au relais se nourrissait de ces verdicts
// et donc d elle-meme.
//
// Mesure en production le 2026-08-31 : sur 119 joueurs relayes, 85% continuaient d annoncer
// FAILED, et le nombre d eligibles est passe de 14 sur 46 a 134 sur 151 en une heure et
// demie — soit, en pratique, le relais pour tout le monde, l etat meme qui avait fait sortir
// huit joueurs en une minute.
//
// Ce registre note quelles paires ont ete relayees, pour que logNATResult sache quand un
// verdict ne dit rien du reseau du joueur.

import (
	"sync"
	"time"
)

// relayVuTTL borne la duree pendant laquelle une paire compte comme relayee.
//
// Assez long pour couvrir la sequence jointure -> percage -> verdict et une boucle de reprise.
// Deliberement PLUS COURT que besoinFenetre (15 min) : sinon un joueur qui rejoue plus tard en
// direct ne serait plus jamais mesure honnetement, et l eligibilite ne pourrait plus decroitre.
const relayVuTTL = 5 * time.Minute

var (
	vuMu     sync.Mutex
	vues     = map[[2]uint64]time.Time{}
	vuMenage time.Time
)

// clePaire nomme une paire independamment de l ordre, comme pairPortFor trie deja les PID.
func clePaire(a, b uint64) [2]uint64 {
	if a > b {
		a, b = b, a
	}

	return [2]uint64{a, b}
}

// NoterPaireRelayee retient que cette paire vient de recevoir un port de relais.
func NoterPaireRelayee(a, b uint64) {
	if a == 0 || b == 0 {
		return
	}

	vuMu.Lock()
	defer vuMu.Unlock()

	maintenant := time.Now()

	// Menage au plus une fois par minute, comme NotePercage : sans lui la carte grandit
	// avec chaque paire jamais revue.
	if maintenant.Sub(vuMenage) > time.Minute {
		for k, t := range vues {
			if maintenant.Sub(t) > relayVuTTL {
				delete(vues, k)
			}
		}
		vuMenage = maintenant
	}

	vues[clePaire(a, b)] = maintenant
}

// PaireRelayee dit si cette paire est passee par le relais assez recemment pour qu un verdict
// de percage ne veuille rien dire.
func PaireRelayee(a, b uint64) bool {
	vuMu.Lock()
	defer vuMu.Unlock()

	t, ok := vues[clePaire(a, b)]

	return ok && time.Since(t) <= relayVuTTL
}
