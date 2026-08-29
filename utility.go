package nex

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Utility protocol. MK8 calls GetIntegerSettings right after registering; we
// return empty setting maps, which is enough to unblock the connection sequence.
const (
	ProtocolUtility          uint16 = 0x6E
	MethodGetIntegerSettings uint32 = 0x7
	MethodGetStringSettings  uint32 = 0x8

	// A title asks for a NEX unique id the first time an account goes online, then keeps
	// it locally — so these are only ever called on a FRESH account, which is why an
	// unanswered one looks like "it works for most people". Splatoon 2 calls method 2 and
	// shows 2306-0103 if it is not answered. Both were implemented in the nex-go stack and
	// lost when the games moved to this core.
	MethodAcquireNexUniqueID             uint32 = 0x1
	MethodAcquireNexUniqueIDWithPassword uint32 = 0x2

	// PAC-MAN 99 ne demande PAS d'id : il suppose qu'il en existe deja un et se contente
	// de demander LEQUEL est le sien, juste apres Register. Sans reponse, la console
	// recommence tout le cycle connexion / Register / question sans jamais avancer —
	// vu comme 2306-0502 a l'ecran, alors que l'authentification, elle, avait reussi.
	MethodAssociateNexUniqueIDWithMyPrincipalID     uint32 = 0x3
	MethodGetAssociatedNexUniqueIDWithMyPrincipalID uint32 = 0x5
)

// nexUniqueIDPasswordSalt derives a unique-id password from the id itself.
//
// The password only has to be stable and to match what we hand out: nothing verifies it
// against a store, because the id IS the account's PID and the account is already
// authenticated by its Kerberos ticket before any of this runs. Same constant as the
// nex-go stack, so an account that acquired its id there keeps working here.
const nexUniqueIDPasswordSalt uint64 = 0x4e45585f50574421

// utilitySondePeuplee : commutateur de l experience ci-dessus, pilote par NEX_UTILITY_SONDE=1.
// utilityNbReglages : NOMBRE d'entrees rendues par GetIntegerSettings (cles 0..n-1,
// valeur 0). Zero — le defaut — rend une carte VIDE : c'est le comportement historique,
// suffisant pour MK8 et les autres, et on ne le change pas sous leurs pieds.
//
// MESURE DU 2026-08-26, PAC-MAN 99, par bissection sur une vraie console :
//
//	carte vide       -> le jeu se tait apres GetIntegerSettings, aucun matchmaking
//	cles 0..15  (16) -> pareil
//	cles 0..19  (20) -> pareil
//	cles 0..20  (21) -> pareil
//	cles 0..21  (22) -> AutoMatchmake enchaine, salon cree, jeu en attente de joueurs
//	la SEULE cle 21  -> ECHEC
//
// Le dernier essai est le plus parlant : fournir la cle 21 isolement ne suffit pas, mais
// la fournir en 22e position suffit. Le client ne fait donc PAS une recherche par cle — il
// lit une POSITION. La valeur, elle, est indifferente : des zeros passent.
//
// On ignore ce que ce reglage represente ; on sait seulement que le jeu refuse d'avancer
// sans lui. A remplacer si un jour une capture du vrai service en donne le contenu reel.
var utilityNbReglages = func() int {
	n, err := strconv.Atoi(os.Getenv("NEX_UTILITY_REGLAGES"))
	if err != nil || n < 0 || n > 4096 {
		return 0
	}
	return n
}()

// utilityValeursReglages : les VALEURS, cle par cle, sous forme "cle=valeur,cle=valeur".
// Toute cle non citee vaut zero.
//
// POURQUOI. On rendait des zeros partout, en se disant que seul le NOMBRE d'entrees
// comptait — c'est ce que la bissection montrait. Mais le serveur SMB35 de kinnay (AGPL,
// lu pour les faits) garnit ces reglages de vraies valeurs : 60, 30, 90, 180 pour les
// unes, 5, 3, 1 pour les autres. Ca ressemble a des DELAIS en secondes et a des comptes
// de joueurs ou de manches.
//
// Or PAC-MAN 99 finit son compte a rebours et ne demarre rien. Un « delai avant depart »
// ou un « minimum de joueurs » lu a zero produirait exactement ca. Reglable pour pouvoir
// le verifier au lieu d'en debattre.
var utilityValeursReglages = analyserValeursReglages(os.Getenv("NEX_UTILITY_VALEURS"))

// utilityClesU32 : ecrire les cles de la carte de reglages en 32 bits au lieu de 16.
var utilityClesU32 = os.Getenv("NEX_UTILITY_CLES_U32") == "1"

// analyserValeursReglages lit une liste "cle=valeur,cle=valeur".
func analyserValeursReglages(brut string) map[int]int32 {
	out := map[int]int32{}
	for _, part := range strings.Split(brut, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		ki, err1 := strconv.Atoi(strings.TrimSpace(k))
		vi, err2 := strconv.Atoi(strings.TrimSpace(v))
		if err1 != nil || err2 != nil || ki < 0 || ki > 0xFFFF {
			continue
		}
		out[ki] = int32(vi)
	}
	return out
}

// utilityReglagesPour rend les reglages d'UN groupe donne.
//
// LE PROBLEME QU'ON CORRIGE. GetIntegerSettings porte un INDEX, et on ne le lisait que pour
// l'ecrire dans le journal : quel que soit le groupe demande, on rendait le meme. Le
// journal de SMB35 montre que le jeu en demande deux — l'index 0 (389 fois) et l'index 10
// (15 fois, systematiquement juste avant de consulter les reglages de la categorie de
// classement 100). Les deux recevaient la meme reponse.
//
// On ne sait PAS ce que le groupe 10 devrait contenir. La correction n'est donc pas de
// deviner une valeur, c'est d'arreter de confondre les deux : chaque index peut desormais
// avoir ses propres reglages, par NEX_UTILITY_REGLAGES_<index> et NEX_UTILITY_VALEURS_<index>.
//
// SANS CONFIGURATION, RIEN NE CHANGE. Un index sans reglages propres retombe sur les
// valeurs globales, c'est-a-dire exactement ce qui est rendu aujourd'hui — l'appariement
// de PAC-MAN 99 et de SMB35 depend de ce groupe 0, et on ne le modifie pas en passant.
// Cela rend simplement le groupe 10 mesurable sans toucher au groupe qui fonctionne.
// UtilityReglagesJeu : les reglages que le JEU fournit lui-meme, quand l'environnement n'en
// donne aucun. Un jeu dont on connait les valeurs n'a pas a dependre d'une variable posee a
// la main sur un conteneur — SMM2 tourne dans un `docker run` nu, ou ajouter une variable
// veut dire le recreer de memoire.
//
// L'ENVIRONNEMENT RESTE PRIORITAIRE : NEX_UTILITY_REGLAGES continue de tout ecraser, et les
// groupes par index aussi. Ceci n'est qu'un defaut, pour ne pas rendre une carte VIDE a un
// jeu qui attend des reglages.
var UtilityReglagesJeu map[int]int32

func utilityReglagesPour(idx int64) (int, map[int]int32) {
	if idx >= 0 {
		suffixe := strconv.FormatInt(idx, 10)
		if n, err := strconv.Atoi(os.Getenv("NEX_UTILITY_REGLAGES_" + suffixe)); err == nil && n >= 0 && n <= 4096 {
			return n, analyserValeursReglages(os.Getenv("NEX_UTILITY_VALEURS_" + suffixe))
		}
	}
	if utilityNbReglages == 0 && len(UtilityReglagesJeu) > 0 {
		// Le nombre d'entrees est la borne haute des cles PLUS UNE : le client lit des
		// POSITIONS, pas des cles, et s'arreter avant la derniere revient a ne pas la
		// fournir du tout. C'est la lecon de PAC-MAN 99, ou la 22e position manquante
		// suffisait a bloquer l'appariement.
		n := 0
		for k := range UtilityReglagesJeu {
			if k+1 > n {
				n = k + 1
			}
		}
		return n, UtilityReglagesJeu
	}
	return utilityNbReglages, utilityValeursReglages
}

// UniqueIDInfo is the Utility structure returned by AcquireNexUniqueIDWithPassword:
// the id plus the password the title stores alongside it.
type UniqueIDInfo struct {
	NEXUniqueID         uint64
	NEXUniqueIDPassword uint64
}

// Levels implements Structure.
func (u *UniqueIDInfo) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.U64(u.NEXUniqueID); o.U64(u.NEXUniqueIDPassword) },
		Load: func(i *StreamIn) { u.NEXUniqueID = i.U64(); u.NEXUniqueIDPassword = i.U64() },
	}}
}

// UtilityHandler answers the settings queries with empty maps, and hands out a NEX unique
// id derived from the caller's PID.
func UtilityHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodGetIntegerSettings, MethodGetStringSettings:
			// On JOURNALISE l'index demande. La reponse « carte vide » suffit a MK8 mais pas a
			// PAC-MAN 99, qui repart en boucle apres l'avoir recue : sans savoir QUEL groupe de
			// reglages il reclame, on ne peut que deviner. Le parametre est un Uint32 unique
			// (index), donc un corps plus court signifie qu'on a mal lu la requete.
			idx := int64(-1)
			if len(req.Body) >= 4 {
				idx = int64(NewStreamIn(req.Body, conn.Settings).U32())
			}
			fmt.Printf("[Utility] settings method=%d index=%d bodyLen=%d -> carte vide\n",
				req.Method, idx, len(req.Body))

			// EXPERIENCE : la carte VIDE suffit a MK8 mais laisse PAC-MAN 99 s arreter net apres
			// l avoir recue, sans plus rien emettre. On ignore quelles cles il lit — les noms de
			// methode NEX ne sont pas des chaines dans le binaire et les vtables n y sont pas
			// resolues. On repond donc une carte PEUPLEE pour trancher UNE question : le contenu
			// compte-t-il ? Si le comportement ne bouge pas, le mur est ailleurs.
			// A RETIRER ensuite : c est une sonde, pas une implementation.
			out := NewStreamOut(conn.Settings)
			nbReglages, valeurs := utilityReglagesPour(idx)
			if req.Method == MethodGetIntegerSettings && nbReglages > 0 {
				out.U32(uint32(nbReglages))
				for k := 0; k < nbReglages; k++ {
					// LARGEUR DE LA CLE. Une Map NEX s'ecrit « u32 compte » puis des paires ;
					// la largeur de la CLE depend de la version de NEX du titre. PAC-MAN 99
					// (2021) accepte u16 — mesure sur console. TETRIS 99 est de 2019 et
					// s'arrete net apres avoir recu cette reponse, sans plus rien emettre ;
					// le CONTENU n'y change rien (vide, 22 ou 64 entrees echouent pareil),
					// donc c'est la FORME qu'il faut essayer.
					if utilityClesU32 {
						out.U32(uint32(k))
					} else {
						out.U16(uint16(k))
					}
					out.U32(uint32(valeurs[k])) // absente = 0
				}
				fmt.Printf("[Utility] carte de reglages groupe=%d : %d entrees, valeurs non nulles=%v\n",
					idx, nbReglages, valeurs)
			} else {
				out.U32(0) // empty Map<u16, ...>
			}
			fmt.Printf("[Utility] reponse method=%d callID=%d corps=% x\n", req.Method, req.CallID, out.Bytes())

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		case MethodAcquireNexUniqueID:
			// Derived from the PID rather than allocated: it must survive restarts and stay
			// the same id across sessions, and the PID already is a stable per-account
			// number we own. Allocating a counter would need storage and would hand a
			// returning account a different id than the one it kept.
			out := NewStreamOut(conn.Settings)
			out.U64(uint64(conn.PID))
			fmt.Printf("[Utility] AcquireNexUniqueID pid=%d -> %d\n", conn.PID, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		case MethodAcquireNexUniqueIDWithPassword:
			out := NewStreamOut(conn.Settings)
			out.Add(&UniqueIDInfo{
				NEXUniqueID:         uint64(conn.PID),
				NEXUniqueIDPassword: uint64(conn.PID) ^ nexUniqueIDPasswordSalt,
			})
			fmt.Printf("[Utility] AcquireNexUniqueIDWithPassword pid=%d -> uid=%d (+pw)\n", conn.PID, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		case MethodAssociateNexUniqueIDWithMyPrincipalID:
			// Le titre ASSOCIE un id unique a son compte. Nos ids etant DERIVES du PID, il
			// n'y a rien a enregistrer : l'association est deja vraie par construction. On
			// acquitte pour ne pas le laisser attendre.
			fmt.Printf("[Utility] AssociateNexUniqueID pid=%d (%d o, deja implicite)\n", conn.PID, len(req.Body))

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, nil)

		case MethodGetAssociatedNexUniqueIDWithMyPrincipalID:
			// Meme reponse que l'acquisition avec mot de passe : nos ids sont DERIVES du PID,
			// jamais alloues, donc la consultation ne peut pas trouver autre chose que ce que
			// l'acquisition aurait rendu — il n'y a rien a stocker et donc rien qui puisse
			// manquer pour un compte qui n'est jamais passe par l'acquisition.
			out := NewStreamOut(conn.Settings)
			out.Add(&UniqueIDInfo{
				NEXUniqueID:         uint64(conn.PID),
				NEXUniqueIDPassword: uint64(conn.PID) ^ nexUniqueIDPasswordSalt,
			})
			fmt.Printf("[Utility] GetAssociatedNexUniqueIDWithMyPrincipalID pid=%d -> uid=%d (+pw)\n", conn.PID, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolUtility, req.Method, req.CallID, out.Bytes())

		default:
			return notImplemented(conn, ProtocolUtility, req)
		}
	}
}
