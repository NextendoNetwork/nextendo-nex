package nex

// Qui a le DROIT d etre relaye.
//
// Distinct de relaybesoin.go, qui dit qui en a besoin. Le besoin est une mesure ; ceci est une
// autorisation, et elle existe parce que la mesure s est trompee trois fois de suite le
// 2026-08-30 : a chaque essai, des joueurs qui n avaient aucun probleme se sont retrouves
// dehors. Tant que le relais n a pas fait ses preuves, il ne doit pouvoir toucher QUE des
// joueurs dont on sait qu ils ne peuvent rien perdre.
//
// Trois proprietes, chacune contre une facon precise de se rater :
//
//  1. Ferme par defaut. Liste absente, vide ou illisible = personne n est relaye, meme si le
//     relais est arme. Perdre le fichier ferme le relais, il ne le fige pas ouvert.
//  2. Les DEUX joueurs doivent y figurer. Le relais est par paire et on ne peut pas n en
//     relayer qu un : accepter sur un seul, c est detourner le trafic de son partenaire, qui
//     n a rien demande.
//  3. Une ligne malformee est ignoree, jamais interpretee comme un joker.

import (
	"fmt"
	"sort"
	"sync"
)

var (
	volMu       sync.RWMutex
	volontaires map[uint64]bool
	volForces   map[uint64]bool
	volOuvert   bool
)

// SetRelayVolontaires remplace la liste. ouvert=true ouvre le relais a tous ceux qui en ont
// besoin — l elargissement final, qui ne doit jamais etre un effet de bord d une liste vide.
func SetRelayVolontaires(pids, forces []uint64, ouvert bool) {
	volMu.Lock()
	defer volMu.Unlock()

	volontaires = make(map[uint64]bool, len(pids))
	for _, p := range pids {
		if p != 0 {
			volontaires[p] = true
		}
	}
	volForces = make(map[uint64]bool, len(forces))
	for _, p := range forces {
		if p != 0 {
			volForces[p] = true
			volontaires[p] = true // forcer implique etre autorise
		}
	}
	volOuvert = ouvert
}

// RelayVolontaire dit si ce joueur peut etre relaye.
func RelayVolontaire(pid uint64) bool {
	volMu.RLock()
	defer volMu.RUnlock()

	return volOuvert || volontaires[pid]
}

// RelayForce dit si ce joueur doit etre relaye sans attendre qu il echoue.
//
// Necessaire pour valider : depuis que les verdicts d une paire relayee ne comptent plus
// (relayvu.go), un volontaire cesse d accumuler des echecs et redeviendrait non eligible en
// pleine session d essai, retombant en direct — indiscernable d un relais qui marche.
func RelayForce(pid uint64) bool {
	volMu.RLock()
	defer volMu.RUnlock()

	return volForces[pid]
}

// RelayVolontairesStats rend le nombre d autorises et l etat d elargissement.
func RelayVolontairesStats() (int, bool) {
	volMu.RLock()
	defer volMu.RUnlock()

	return len(volontaires), volOuvert
}

// RelayVolontairesListe rend les PID autorises, tries, pour le tableau de bord et le journal.
func RelayVolontairesListe() []uint64 {
	volMu.RLock()
	defer volMu.RUnlock()

	out := make([]uint64, 0, len(volontaires))
	for p := range volontaires {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

// journalVolontaires rend une description courte, pour la ligne de journal du changement.
func journalVolontaires() string {
	n, ouvert := RelayVolontairesStats()
	if ouvert {
		return fmt.Sprintf("%d autorises + ELARGI A TOUS", n)
	}

	return fmt.Sprintf("%d autorises", n)
}
