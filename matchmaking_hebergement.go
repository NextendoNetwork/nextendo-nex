package nex

import "fmt"

// MigrerHebergementDepuis transfere l'hebergement des salons tenus par ce PID vers un
// participant encore joignable, SANS retirer personne du salon.
//
// POURQUOI C'EST SEPARE DU RETRAIT.
//
// Un joueur deconnecte garde sa place quelques secondes, le temps que sa console rouvre sa
// connexion : sans ce delai les salons se vidaient a chaque va-et-vient. Mais garder sa
// PLACE et garder son POSTE D'HOTE sont deux choses differentes, et confondre les deux a
// coute un plantage.
//
// Ce que ca donnait, mesure sur SMM2 le 2026-09-02 a partir d'un rapport de plantage
// d'Atmosphere : GetSessionURLs ne trouvait plus la connexion de l'hote et repondait une
// liste de stations VIDE. La console partait alors en migration d'hote — la pile du
// plantage nomme NexProcessHostMigrationJob::WaitNewHostGreeting — et parcourait une
// collection de stations dont la racine valait 0x10. Data Abort, le jeu se ferme au moment
// ou le joueur franchit l'arrivee.
//
// La regle qui en sort : un salon doit TOUJOURS etre heberge par quelqu'un de joignable.
// Le delai de grace s'applique a l'appartenance, jamais a l'hebergement.
//
// Rend le nombre de salons migres. Silencieux quand il n'y a rien a faire, ce qui est le
// cas courant.
func (m *Matchmaking) MigrerHebergementDepuis(pid uint64, ep *Endpoint) int {
	if pid == 0 || ep == nil {
		return 0
	}

	type migre struct {
		gid            uint32
		ancien, nouvel uint64
	}
	var faits []migre

	m.mu.Lock()
	for gid, g := range m.gatherings {
		if g == nil || g.session == nil {
			continue
		}
		if g.session.HostPID != pid && g.session.OwnerPID != pid {
			continue
		}

		// Le remplacant doit etre joignable MAINTENANT : reprendre l'hebergement au profit
		// d'un second absent ne ferait que deplacer le probleme.
		var repreneur *Connection
		for _, p := range g.participants {
			if p == pid {
				continue
			}
			if c := ep.FindConnectionByPID(p); c != nil {
				repreneur = c

				break
			}
		}
		if repreneur == nil {
			// Personne pour reprendre : on ne touche a rien. Le salon disparaitra de
			// lui-meme quand le delai de grace expirera, et d'ici la il vaut mieux un
			// salon fige qu'un salon dont l'hote annonce est un autre absent.
			continue
		}

		faits = append(faits, migre{gid, pid, repreneur.PID})
		g.session.HostPID = repreneur.PID
		g.session.OwnerPID = repreneur.PID
		g.hostConnID = repreneur.ID

		// index 0 = hote, par convention de ce fichier : on remet le repreneur en tete.
		reordonne := make([]uint64, 0, len(g.participants))
		reordonne = append(reordonne, repreneur.PID)
		for _, p := range g.participants {
			if p != repreneur.PID {
				reordonne = append(reordonne, p)
			}
		}
		g.participants = reordonne
	}
	m.mu.Unlock()

	// AVERTIR LES AUTRES, sinon migrer ne sert a rien.
	//
	// La console qui reprend et celles qui restent lancent leur propre travail de migration
	// et attendent d'apprendre QUI est le nouvel hote — la pile du plantage nomme
	// NexProcessHostMigrationJob::WaitNewHostGreeting. Migrer en silence cote serveur les
	// laisse attendre indefiniment : le jeu ne plante plus, il se fige, ce qui est le meme
	// defaut sous un autre visage.
	//
	// C'est exactement l'avis que RemovePlayer emet deja quand le proprietaire s'en va ; on
	// emet le meme, avec les memes champs, pour que les deux chemins soient indiscernables
	// vu du client.
	//
	// Les avis partent APRES le verrou : SendNotification ecrit sur une socket et peut
	// bloquer, et tenir m.mu pendant ce temps figerait l'appariement de tout le monde.
	for _, f := range faits {
		fmt.Printf("[MM] hebergement gid=%d : %d injoignable -> repris par %d\n", f.gid, f.ancien, f.nouvel)

		m.mu.Lock()
		var parts []uint64
		if g := m.gatherings[f.gid]; g != nil {
			parts = append([]uint64(nil), g.participants...)
		}
		m.mu.Unlock()

		for _, p := range parts {
			c := ep.FindConnectionByPID(p)
			if c == nil {
				continue
			}
			SendNotification(c, &NotificationEvent{
				PIDSource: f.ancien,
				Type:      NotificationOwnershipChanged,
				Param1:    uint64(f.gid),
				Param2:    f.nouvel,
			})
		}
	}

	return len(faits)
}

// migrerSalonApresSortie: gid's owner has just LEFT it (EndParticipation) while others remain.
// Same rule and same notice as MigrerHebergementDepuis, but for THIS gathering only: the
// leaver may still host other gatherings it is in.
//
// SSBU prod, 10/17: 7 hosts left their Quick Play gathering ~3 s after a joiner arrived. The
// gathering kept the departed host as owner; the joiner probed an absent host (InitiateProbe
// -> 0x80010002 NotImplemented, "hote muet", up to 25 s of waiting) and the gathering stayed
// offered to autoMatchmake (09:18:26 UTC, the departed host was put back into it). The original
// service hands the gathering to a remaining participant when flag 0x10 is set (Smash: flags
// 0xA10), otherwise deletes it.
//
// Returns true if the gathering was handed over.
func (m *Matchmaking) migrerSalonApresSortie(gid uint32, ancien uint64, ep *Endpoint) bool {
	if ep == nil {
		return false
	}
	m.mu.Lock()
	g := m.gatherings[gid]
	if g == nil || g.session == nil || (g.session.OwnerPID != ancien && g.session.HostPID != ancien) {
		m.mu.Unlock()
		return false
	}
	var repreneur *Connection
	for _, p := range g.participants {
		if p == ancien {
			continue
		}
		if c := ep.FindConnectionByPID(p); c != nil {
			repreneur = c
			break
		}
	}
	if repreneur == nil {
		m.mu.Unlock()
		return false
	}
	g.session.HostPID = repreneur.PID
	g.session.OwnerPID = repreneur.PID
	g.hostConnID = repreneur.ID
	reordonne := []uint64{repreneur.PID}
	for _, p := range g.participants {
		if p != repreneur.PID && p != ancien {
			reordonne = append(reordonne, p)
		}
	}
	g.participants = reordonne
	g.session.NumParticipants = uint32(len(reordonne))
	parts := append([]uint64(nil), reordonne...)
	m.mu.Unlock()

	fmt.Printf("[MM] hebergement gid=%d: owner %d left (EndParticipation) -> taken over by %d\n", gid, ancien, repreneur.PID)
	for _, p := range parts {
		if c := ep.FindConnectionByPID(p); c != nil {
			SendNotification(c, &NotificationEvent{
				PIDSource: ancien,
				Type:      NotificationOwnershipChanged,
				Param1:    uint64(gid),
				Param2:    repreneur.PID,
			})
		}
	}
	return true
}
