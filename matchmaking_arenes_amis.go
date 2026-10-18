package nex

// Arenes d amis de Smash : FindMatchmakeSessionByParticipant (0x6D.0x33, 51).
//
// Le menu des arenes de Smash appelle cette methode pour remplir la liste « Arenes d amis » :
// Pia NexMatchBrowseMatchmakeJob 0x1068d0 (chaine « WaitFindSessionByParticipant »
// referencee en 0x1069fc) -> 0xea3e0 (appel en 0xea64c) -> 0x2503b0 -> 0x2503c0,
// SetMethodID 0x33 en 0x2504b0. Le journal de prod le montre depuis le menu, pas seulement au
// demarrage (7 appels du meme joueur entre deux 0x6D.0x34, puis jonction 0x6D.0x27). Le coeur
// repondait toujours une liste vide : personne ne voyait jamais l arene d un ami.
//
// Format prouve dans le main 13.0.5 :
//   - requete, serialiseur 0x252870 (appele en 0x2504bc) : en-tete de structure, u32 nombre
//     puis un u64 par identifiant (liste chainee, valeur en +0x10 du noeud,
//     0x2528e4-0x252924), u32 options (+0x28, 0x252928), MatchmakeBlockListParam (0x252cf0) :
//     c est FindMatchmakeSessionByParticipantParam (matchmaking_acnh.go) ;
//   - reponse : 0x2503c0 pose l extracteur de la methode 51 dans la table [0x52a7b78] (+0x190 =
//     indice 50, valeur 0x253ae0 lue en GOT 0x52a7bf0, aiguillage 0x253fd0) ; 0x253ae0 ->
//     0x257d60 lit un u32 nombre puis, par element, 0x2529a0 : en-tete de structure, u64
//     (+0x10, 0x252a88-0x252abc) puis MatchmakeSession (0x254d50, le meme lecteur que la
//     liste de 0x6D.0x34 via 0x257720 -> 0x2578b0). La reponse est donc
//     List<FindMatchmakeSessionByParticipantResult{PID, MatchmakeSession}>, comme pour
//     Animal Crossing, et non une liste de sessions nues. Verifie en passant la reponse de
//     ce fichier dans le vrai extracteur 0x253ae0 sous Unicorn
//     (un outil interne).
//
// Les amis viennent de nn::friends (GetFriendList 0x3756c34, Friend::GetAccountId
// 0x3756d30) : sur une vraie Switch l identifiant demande est peut-etre le NSA et non le PID
// (hypothese, le corps de 0x6D.0x33 n etait pas journalise). On accepte donc les deux : le
// PID tel quel, sinon la traduction NSA -> PID apprise a l authentification
// (PIDForLoginName, ticketgranting.go). L emulateur Nextendo presente le PID comme NSA.
//
// Ce que la liste rend, pour chaque identifiant demande : les arenes ou ce joueur est
// present, creees par CreateMatchmakeSessionWithParam (0x6D.0x26, gathering.arene), ouvertes
// (OpenParticipation), jamais une session isolee (ModeIsole, match de tournoi) ni une session
// au mot de code de tournoi « T- », jamais un salon de Partie rapide (cree par 0x6D.0x28).
// Ni cle de session ni mot de passe : la cle d une arene sort a la jonction (0x27), comme
// pour la recherche publique 0x34 ; le cadenas (UserPasswordEnabled) reste, pour que le jeu
// demande le code d acces.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// accessKeySSBU : la cle d acces NEX du serveur Smash (super-smash-bros-ultimate/main.go).
// Les arenes d amis ne s activent que pour elle, les autres jeux gardent la liste vide.
const accessKeySSBU = "9587602b"

// arenesAmisMaxIDs : une Switch a au plus 300 amis ; au-dela, le reste est ignore.
const arenesAmisMaxIDs = 300

// arenesAmisActives : la variante Smash de 0x6D.0x33 s applique a cette connexion.
func (m *Matchmaking) arenesAmisActives(conn *Connection) bool {
	return !m.SansArenesAmis && conn != nil && conn.Settings != nil && conn.Settings.AccessKey == accessKeySSBU
}

// areneAmisVisible : l arene peut figurer dans la liste des amis. Verrou m.mu tenu.
func areneAmisVisible(g *gathering) bool {
	if g == nil || g.session == nil || !g.arene || len(g.participants) == 0 {
		return false
	}
	if !g.session.OpenParticipation || ModeIsole(g.session.GameMode) {
		return false
	}
	if strings.HasPrefix(g.session.Codeword, "T-") || strings.HasPrefix(g.code, "T-") {
		return false
	}

	return true
}

// demandesArenesAmis rend, pour chaque PID NEX vise, les identifiants demandes qui le
// designent (le PID lui-meme, ou un NSA traduit par l authentification, PIDForLoginName ;
// plusieurs identifiants peuvent viser le meme PID), et le nombre d identifiants traduits
// NSA -> PID. Ce nombre est journalise : sans lui, la prod ne dit pas si la branche NSA sert
// vraiment (prod du 17/10 00:30Z a 03:40Z : 115 lignes « arenes d amis », 36 reponses non vides, sans
// savoir si l identifiant demande etait un NSA ou un PID).
func demandesArenesAmis(ids []uint64) (map[uint64][]uint64, int) {
	demandes := make(map[uint64][]uint64, len(ids))
	vus := make(map[uint64]bool, len(ids))
	traduits := 0
	for _, id := range ids {
		if id == 0 || vus[id] {
			continue
		}
		vus[id] = true
		demandes[id] = append(demandes[id], id)
		if pid, ok := PIDForLoginName(strconv.FormatUint(id, 10)); ok && pid != 0 && pid != id {
			demandes[pid] = append(demandes[pid], id)
			traduits++
		}
	}

	return demandes, traduits
}

// findArenesAmis repond a FindMatchmakeSessionByParticipant pour Smash.
func (m *Matchmaking) findArenesAmis(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings

	var param FindMatchmakeSessionByParticipantParam
	in := NewStreamIn(req.Body, s)
	in.Extract(&param)
	if in.Err() != nil {
		fmt.Printf("[MM] arenes d amis pid=%d : requete illisible (%d octets) -> liste vide\n", conn.PID, len(req.Body))

		return emptyList(conn, ProtocolMatchmakeExtension, req)
	}
	ids := param.PrincipalIDs
	if len(ids) > arenesAmisMaxIDs {
		fmt.Printf("[MM] arenes d amis pid=%d : %d identifiants, %d gardes\n", conn.PID, len(ids), arenesAmisMaxIDs)
		ids = ids[:arenesAmisMaxIDs]
	}

	demandes, traduits := demandesArenesAmis(ids)

	var results []*FindMatchmakeSessionByParticipantResult
	m.mu.Lock()
	gids := make([]uint32, 0, len(m.gatherings))
	for gid, g := range m.gatherings {
		if areneAmisVisible(g) && !g.orphelin {
			gids = append(gids, gid)
		}
	}
	sort.Slice(gids, func(i, j int) bool { return gids[i] < gids[j] })
	for _, gid := range gids {
		g := m.gatherings[gid]
		if g == nil {
			continue
		}
		for _, p := range g.participants {
			for _, id := range demandes[p] {
				r := *g.session
				expurgerSessionProtegee(&r)
				r.SessionKey = nil
				results = append(results, &FindMatchmakeSessionByParticipantResult{PrincipalID: id, Session: r})
			}
		}
	}
	m.mu.Unlock()

	out := NewStreamOut(s)
	out.U32(uint32(len(results)))
	for _, r := range results {
		out.Add(r)
	}
	fmt.Printf("[MM] arenes d amis pid=%d : %d identifiant(s) demande(s) -> %d arene(s), %d identifiant(s) traduit(s) NSA->PID\n", conn.PID, len(ids), len(results), traduits)

	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}
