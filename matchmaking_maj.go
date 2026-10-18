package nex

import "fmt"

// Mise a jour de session par Smash : MatchmakeExtension UpdateMatchmakeSessionPart (0x6D.0x2C).
//
// CE QU'ENVOIE LE JEU (Smash 13.0.5). Deux constructeurs cote Smash.
//
// 0x3708c70 (appele par 0x3712674, fonction 0x3712640) : il remplit un UpdateSessionSetting Pia a partir du MEME reglage de 0xe8 octets
// que la creation de session (0x168ae00), puis Pia le convertit (0xe4ac0, job
// NexMatchUpdateSessionSettingJob, appel 0x10d7ac) et l'envoie (0xe6470 -> 0x24f6b0 ->
// 0x24f6c0, methode 0x2c en 0x24f7a4, serialiseur 0x256dd0). Les drapeaux de modification
// sont ceux des setters Pia, recopies tels quels (0xe4b00-0xe4b1c, 0x116160) :
//
//	0x0001 attributs (0x115930, les 6 valeurs du reglage)
//	0x0002 OpenParticipation (0xe4b04-0xe4b14, bit 0 du job +0x2c4)
//	0x0040 MatchmakeParam (pays @CC 0x115c60, @RV/@DR/@VR 0x115ba0/0x115be0/0x115c20,
//	       regles « _RU » 0x115da0)
//	0x0800 MinParticipants (0x115880), 0x1000 MaxParticipants (0x1158b0)
//	0x2000 MatchmakeSystem, ParticipationPolicy, PolicyArgument (0x1158e0, recopies en
//	       0xe4b44-0xe4ba8)
//	0x0100 mot de passe (0x115a10), 0x4000 mot de code (0x115ad0), si le reglage en porte.
//
// Vecteurs : testdata/appariement/maj_*.hex (un outil interne) :
// drapeaux 0x3841, et 0x3843 quand la participation est rouverte.
//
// 0x3708fa0 (appele par 0x371273c, fonction 0x3712710, voisine de la precedente) : un reglage
// neuf (0x1152f0) ou il ne pose que l'heure de debut, lue de l'horloge (0x1b08c0..0x1b0910 :
// annee, mois, jour, heure, minute, seconde), par 0x116510 (0x3709198), seul setter qui pose le
// drapeau 0x80 (0x1165a0-0x1165ac, StartedTime, ignore ici) ; le job y ajoute 0x2 et la valeur
// de OpenParticipation de son option (0xe4b04-0xe4b1c, recopiee en +0x30 en 0xe4b40). D'ou les
// drapeaux 0x82 vus en prod le 17/10 (ouverte=true), 0,6 s apres un 0x3841 : le jeu rouvre
// lui-meme la participation de son salon a ce moment-la ; on applique ce qu'il demande.
//
// CE QUI SE PASSAIT. Le coeur acquittait sans rien ecrire (SessionPartPersists faux pour
// Smash) : une session gardait les attributs (tranche de niveau, Elite), le format et l'etat
// ouvert/ferme de sa creation, alors que le jeu les met a jour (582 appels en 8 h le 16/10, juste
// apres 0x6D.0x1 dans le cycle de la Partie rapide).
//
// CE QU'ON FAIT. Pour le proprietaire ou l'hote de la session, s'il en est encore participant
// (le proprietaire d'un salon orphelin, parti, ne le modifie plus), et jamais pour une session
// isolee (match de tournoi) : attributs (0x1), OpenParticipation (0x2), parametres (0x40),
// Min/MaxParticipants (0x800/0x1000, jamais un maximum sous le nombre de presents),
// MatchmakeSystem et la politique (0x2000). Le mot de passe et le mot de code ne changent pas
// (comportement d'avant : frein des essais, arenes).
//
// BORNES (une requete fait jusqu'a 1 Mio et le jeu peut la repeter sans fin) :
//   - attributs : appliques seulement s'il y en a de 1 a 6 (0x115930 en ecrit 6) ;
//   - parametres : seulement les cinq cles que 0x3708c70 pose (0x115c60 @CC, 0x115be0 @DR,
//     0x115ba0 @RV, 0x115c20 @VR, et un seul 0x115da0, indice 0, « _RU », 0x3708e9c-0x3708ec0 ;
//     vecteurs maj_*.hex : ces cinq cles et une cle vide), une chaine de 16 octets au plus.
//     Toute autre cle est ignoree : la session ne grossit jamais au fil des mises a jour (avant,
//     chaque cle recue s'ajoutait, 40 requetes de 0,8 Mo = 112 Mo de tas) ;
//   - journal : chaque champ variable est tronque (texteAttribs, texteChaine, texteParam).
const (
	majAttributs     uint32 = 0x0001
	majOuverte       uint32 = 0x0002
	majParam         uint32 = 0x0040
	majMinPart       uint32 = 0x0800
	majMaxPart       uint32 = 0x1000
	majSysteme       uint32 = 0x2000
	majAppliquesMask        = majAttributs | majOuverte | majParam | majMinPart | majMaxPart | majSysteme

	// majAttribsMax : nombre d'attributs d'une session Smash (0x115930).
	majAttribsMax = 6
	// majChaineMax : longueur maximale d'un parametre chaine applique (@CC = code pays, « FR »).
	majChaineMax = 16
)

// clesMajSmash : les seules cles de parametres que Smash met a jour par 0x6D.0x2C (voir plus haut).
var clesMajSmash = map[string]bool{"@CC": true, "@DR": true, "@RV": true, "@VR": true, "_RU": true}

// lireMajSession lit une requete 0x6D.0x2C.
func lireMajSession(body []byte, s *Settings) (UpdateMatchmakeSessionParam, bool) {
	var p UpdateMatchmakeSessionParam
	in := newStreamInStrict(body, s)
	in.Extract(&p)
	return p, in.Err() == nil
}

// journalMajSession : contenu de 0x6D.0x2C pour un PID de test (JournalAppariement).
func journalMajSession(conn *Connection, p *UpdateMatchmakeSessionParam, lu bool) {
	fmt.Println(texteMajSession(conn.PID, p, lu))
}

// texteMajSession : ligne du journal de 0x6D.0x2C, chaque champ variable tronque (attributs,
// mot de code, parametres) : quelques centaines d'octets quoi que le client envoie.
func texteMajSession(pid uint64, p *UpdateMatchmakeSessionParam, lu bool) string {
	return fmt.Sprintf("[MM] maj 0x2c pid=%d lu=%v gid=%d drapeaux=%#x attribs=%s ouverte=%v min=%d max=%d sys=%d politique=%d/%d code=%s mdpLen=%d param=%s",
		pid, lu, p.GID, p.ModificationFlags, texteAttribs(p.Attributes), p.OpenParticipation, p.MinParticipants, p.MaxParticipants,
		p.MatchmakeSystem, p.ParticipationPolicy, p.PolicyArgument, texteChaine(p.Codeword), len(p.UserPassword), texteParam(p.Param.Params))
}

// appliquerMajSmash applique une requete 0x6D.0x2C lue (voir plus haut). Rend ce qui a ete fait,
// pour le journal.
func (m *Matchmaking) appliquerMajSmash(conn *Connection, p *UpdateMatchmakeSessionParam) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gatherings[p.GID]
	switch {
	case g == nil || g.session == nil:
		return "salon inconnu"
	case ModeIsole(g.session.GameMode):
		return "session isolee, rien"
	case conn.PID != g.session.OwnerPID && conn.PID != g.session.HostPID:
		return fmt.Sprintf("refuse (proprietaire %d, hote %d)", g.session.OwnerPID, g.session.HostPID)
	case !containsPID(g.participants, conn.PID):
		return "refuse (hors du salon)"
	}
	ss := g.session
	f := p.ModificationFlags
	var ecartes []string
	if f&majAttributs != 0 {
		if n := len(p.Attributes); n > 0 && n <= majAttribsMax {
			ss.Attribs = append(make([]uint32, 0, majAttribsMax), p.Attributes...)
			for len(ss.Attribs) < majAttribsMax {
				ss.Attribs = append(ss.Attribs, 0)
			}
		} else {
			ecartes = append(ecartes, fmt.Sprintf("%d attributs", n))
		}
	}
	if f&majOuverte != 0 {
		ss.OpenParticipation = p.OpenParticipation
	}
	if f&majParam != 0 {
		hors := 0
		for k, v := range p.Param.Params {
			if k == "" {
				continue // emplacement vide de la table Pia (vecteurs maj_*.hex)
			}
			if !clesMajSmash[k] || (v.Type == VariantString && len(v.String) > majChaineMax) {
				hors++
				continue
			}
			if ss.Param.Params == nil {
				ss.Param.Params = map[string]Variant{}
			}
			ss.Param.Params[k] = v
		}
		if hors > 0 {
			ecartes = append(ecartes, fmt.Sprintf("%d parametre(s)", hors))
		}
	}
	if f&majMaxPart != 0 && p.MaxParticipants != 0 && int(p.MaxParticipants) >= len(g.participants) {
		ss.MaxParticipants = p.MaxParticipants
	}
	if f&majMinPart != 0 && p.MinParticipants <= ss.MaxParticipants {
		ss.MinParticipants = p.MinParticipants
	}
	if f&majSysteme != 0 {
		ss.MatchmakeSystem = p.MatchmakeSystem
		ss.ParticipationPolicy = p.ParticipationPolicy
		ss.PolicyArgument = p.PolicyArgument
	}
	return fmt.Sprintf("applique (attribs=%s ouverte=%v min=%d max=%d, drapeaux ignores %#x, ecartes %v)",
		texteAttribs(ss.Attribs), ss.OpenParticipation, ss.MinParticipants, ss.MaxParticipants, f&^majAppliquesMask, ecartes)
}
