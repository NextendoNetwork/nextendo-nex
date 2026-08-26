package nex

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// MatchmakeReferee (protocole 120) — l'arbitre de manche.
//
// SUPER MARIO BROS. 35 ouvre une manche arbitree juste avant de jouer : il demande au
// serveur d'en creer une et attend son numero. Sans reponse, la partie ne demarre pas,
// alors meme que le relais Eagle a deja les deux joueurs en ligne.
//
// Le serveur ne fait qu'ATTRIBUER un numero. Rien ne le lui redemande ensuite, et les
// rapports de fin de manche ne servent qu'aux classements — qu'on ne tient pas encore.
// C'est donc volontairement minimal : de quoi laisser la partie commencer, pas de quoi
// noter qui a gagne.
const (
	ProtocolMatchmakeReferee uint16 = 0x78

	MethodRefereeStartRound            uint32 = 1
	MethodRefereeGetStartRoundParam    uint32 = 2
	MethodRefereeEndRound              uint32 = 3
	MethodRefereeEndRoundPartialReport uint32 = 4
	MethodRefereeEndRoundWithoutReport uint32 = 5
)

// compteurManches : les numeros de manche doivent etre uniques, rien de plus. Un
// compteur atomique suffit et survit a tout sauf a un redemarrage — auquel cas les
// manches en cours sont de toute facon perdues.
var compteurManches uint64

// manches garde les PARAMETRES de chaque manche ouverte. Le jeu les redemande juste
// apres, par GetStartRoundParam, en ne donnant que le numero — et si on ne les lui rend
// pas il s'arrete sur 2306-0103.
//
// On conserve le corps de la requete TEL QUEL et on le rend a l'identique. Le serveur n'a
// aucune raison de comprendre ce qu'il contient : il n'en modifie rien.
var (
	manchesMu sync.Mutex
	manches   = map[uint64][]byte{}
)

// MatchmakeRefereeHandler rend le gestionnaire du protocole 120.
func MatchmakeRefereeHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodRefereeStartRound:
			// On n'interprete PAS le parametre. Il porte la categorie, le salon, la liste
			// des joueurs et un mode de rapport ; rien de tout cela ne change le numero
			// qu'on rend, et le decoder mal casserait un appel qui marche.
			manche := atomic.AddUint64(&compteurManches, 1)
			manchesMu.Lock()
			manches[manche] = append([]byte(nil), req.Body...)
			manchesMu.Unlock()

			// LE POINT QUI MANQUAIT.
			//
			// Rendre le numero de manche a celui qui l'ouvre ne suffit pas : le vrai service
			// PREVIENT chaque joueur de la manche que la partie commence (type 116000). Sans
			// cet avis, chaque pair emet ses deux messages d'entree et se tait — il attend un
			// depart de manche qui n'arrive jamais. Onze consoles, onze fois exactement deux
			// messages : le symptome le plus deterministe de la nuit.
			for _, pid := range refereePIDs(req.Body, conn.Settings) {
				c := RefereeEndpoint
				if c == nil {
					break
				}
				if t := c.FindConnectionByPID(pid); t != nil {
					SendNotification(t, &NotificationEvent{
						PIDSource: conn.PID,
						Type:      NotificationRefereeRoundStarted,
						Param1:    manche,
					})
				}
			}

			out := NewStreamOut(conn.Settings)
			out.U64(manche)
			fmt.Printf("[Referee] manche %d ouverte par pid=%d, %d joueur(s) prevenu(s)\n",
				manche, conn.PID, len(refereePIDs(req.Body, conn.Settings)))

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, out.Bytes())

		case MethodRefereeGetStartRoundParam:
			// Le corps ne porte que le numero de manche.
			manche := NewStreamIn(req.Body, conn.Settings).U64()
			manchesMu.Lock()
			param, ok := manches[manche]
			manchesMu.Unlock()
			if !ok {
				fmt.Printf("[Referee] manche %d inconnue (demandee par pid=%d)\n", manche, conn.PID)
				return notImplemented(conn, ProtocolMatchmakeReferee, req)
			}
			fmt.Printf("[Referee] parametres de la manche %d rendus a pid=%d (%d o)\n",
				manche, conn.PID, len(param))

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, param)

		case MethodRefereeEndRound, MethodRefereeEndRoundPartialReport:
			// Fin de manche AVEC rapport. C'est d'ici que sortent les bilans : chaque joueur
			// y remonte son resultat et sa variation de rating. On les jetait, et c'est
			// pourquoi le jeu n'avait jamais rien a afficher.
			var p MatchmakeRefereeEndRoundParam
			NewStreamIn(req.Body, conn.Settings).Extract(&p)

			cat := refereeCategorie(mancheParam(p.RoundID), conn.Settings)
			for _, res := range p.PersonalRoundResults {
				StatsArbitre.appliquerResultat(cat, res)
			}
			StatsArbitre.Ecrire()
			fmt.Printf("[Referee] manche %d terminee (methode %d) par pid=%d : %d rapport(s), categorie=%d\n",
				p.RoundID, req.Method, conn.PID, len(p.PersonalRoundResults), cat)

			// CE QUE LA MESURE A DONNE, le 2026-08-26, sur des manches reelles.
			//
			// On cherchait d'ou pouvait venir un score, puisque le journal de production
			// montre que ce jeu n'appelle JAMAIS PutScore — zero ligne « non traitee » sur
			// des milliers. Le rapport de fin de manche etait le dernier candidat.
			//
			// Reponse : il n'en vient pas. Le jeu envoie bien un rapport par joueur, avec le
			// bon PID — la structure est donc lue correctement — mais flag, winLoss,
			// variation de rating et tampon sont TOUS a zero. SMB35 ne declare aucun
			// resultat a NEX.
			//
			// Le classement, s'il doit exister un jour, ne peut donc pas se construire ici.
			// Le seul endroit qui connaisse le deroulement d'une partie est le relais Eagle,
			// qui voit passer les RPC de jeu (26-40 : JoinMatch, Dead, Feat...).
			//
			// On ne trace donc plus que l'ANORMAL : si un rapport arrive un jour rempli,
			// cette ligne le dira. Le cas normal reste silencieux.
			for _, res := range p.PersonalRoundResults {
				if res.PersonalRoundResultFlag == 0 && res.RoundWinLoss == 0 &&
					res.RatingValueChange == 0 && len(res.Buffer) == 0 {
					continue
				}
				n := len(res.Buffer)
				if n > 32 {
					n = 32
				}
				fmt.Printf("[Referee]   rapport NON VIDE pid=%d flag=%d winLoss=%d rating%+d tampon=%do % x\n",
					res.PID, res.PersonalRoundResultFlag, res.RoundWinLoss,
					res.RatingValueChange, len(res.Buffer), res.Buffer[:n])
			}

			// Reponse VIDE : le client verifie qu'il a tout lu.
			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, nil)

		case MethodRefereeEndRoundWithoutReport:
			// Sans rapport : rien a comptabiliser, seulement a acquitter.
			fmt.Printf("[Referee] manche terminee sans rapport, pid=%d\n", conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, nil)

		case MethodRefereeGetRoundParticipants:
			manche := NewStreamIn(req.Body, conn.Settings).U64()
			pids := refereePIDs(mancheParam(manche), conn.Settings)
			out := NewStreamOut(conn.Settings)
			out.U32(uint32(len(pids)))
			for _, pid := range pids {
				out.PID(pid)
			}
			fmt.Printf("[Referee] participants de la manche %d rendus a pid=%d : %d\n", manche, conn.PID, len(pids))

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, out.Bytes())

		case MethodRefereeCreateStats:
			var p MatchmakeRefereeStatsInitParam
			NewStreamIn(req.Body, conn.Settings).Extract(&p)
			StatsArbitre.obtenirOuCreer(conn.PID, p.Category, p.InitialRatingValue)
			StatsArbitre.Ecrire()
			fmt.Printf("[Referee] bilan cree pid=%d categorie=%d rating=%d\n", conn.PID, p.Category, p.InitialRatingValue)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, nil)

		case MethodRefereeGetOrCreateStats:
			var p MatchmakeRefereeStatsInitParam
			NewStreamIn(req.Body, conn.Settings).Extract(&p)
			StatsArbitre.obtenirOuCreer(conn.PID, p.Category, p.InitialRatingValue)
			StatsArbitre.Ecrire()
			s := StatsArbitre.lire(conn.PID, p.Category)
			out := NewStreamOut(conn.Settings)
			out.Add(&s)
			fmt.Printf("[Referee] bilan rendu pid=%d categorie=%d : %dV %dD %dN rating=%d\n",
				conn.PID, p.Category, s.TotalWin, s.TotalLoss, s.TotalDraw, s.RatingValue)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, out.Bytes())

		case MethodRefereeGetStatsPrimary:
			var t MatchmakeRefereeStatsTarget
			NewStreamIn(req.Body, conn.Settings).Extract(&t)
			s := StatsArbitre.lire(t.PID, t.Category)
			out := NewStreamOut(conn.Settings)
			out.Add(&s)
			fmt.Printf("[Referee] bilan de pid=%d categorie=%d rendu a pid=%d\n", t.PID, t.Category, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, out.Bytes())

		case MethodRefereeGetStatsPrimaries:
			in := NewStreamIn(req.Body, conn.Settings)
			n := in.U32()
			if n > 128 {
				return notImplemented(conn, ProtocolMatchmakeReferee, req)
			}
			cibles := make([]MatchmakeRefereeStatsTarget, n)
			for idx := range cibles {
				in.Extract(&cibles[idx])
			}
			out := NewStreamOut(conn.Settings)
			out.U32(n)
			for _, t := range cibles {
				s := StatsArbitre.lire(t.PID, t.Category)
				out.Add(&s)
			}
			// La SECONDE liste : un code de resultat par cible. On les rend tous a succes —
			// un bilan absent n'est pas une erreur, c'est un joueur qui n'a pas encore joue,
			// et on renvoie alors un bilan a zero.
			out.U32(n)
			for i := uint32(0); i < n; i++ {
				out.Result(0x00010001) // Core::Success
			}
			fmt.Printf("[Referee] %d bilan(s) rendus a pid=%d\n", n, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, out.Bytes())

		case MethodRefereeGetStatsAll:
			var t MatchmakeRefereeStatsTarget
			NewStreamIn(req.Body, conn.Settings).Extract(&t)
			tous := StatsArbitre.toutesCategories(t.PID)
			out := NewStreamOut(conn.Settings)
			out.U32(uint32(len(tous)))
			for idx := range tous {
				out.Add(&tous[idx])
			}
			fmt.Printf("[Referee] tous les bilans de pid=%d rendus : %d categorie(s)\n", t.PID, len(tous))

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, out.Bytes())

		case MethodRefereeResetStats:
			n := StatsArbitre.reinitialiser(conn.PID)
			StatsArbitre.Ecrire()
			fmt.Printf("[Referee] bilans remis a zero pour pid=%d : %d categorie(s)\n", conn.PID, n)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, nil)

		default:
			fmt.Printf("[Referee] methode %d non traitee pid=%d bodyLen=%d corps=% x\n",
				req.Method, conn.PID, len(req.Body), req.Body)

			return notImplemented(conn, ProtocolMatchmakeReferee, req)
		}
	}
}

// RefereeEndpoint sert a joindre les joueurs d'une manche. Pose par le serveur de jeu.
var RefereeEndpoint *Endpoint

// mancheParam rend le parametre garde a l'ouverture d'une manche, ou nil.
func mancheParam(manche uint64) []byte {
	manchesMu.Lock()
	defer manchesMu.Unlock()
	return manches[manche]
}

// refereeCategorie extrait la categorie de donnees personnelles du parametre de
// StartRound. C'est le premier champ, juste apres l'entete de structure.
//
// Elle sert de CLE aux bilans : sans elle, les statistiques de toutes les categories se
// melangeraient dans une seule. Zero quand la manche est inconnue — un bilan range sous
// une mauvaise categorie vaut mieux qu'un plantage, et le journal le dit.
func refereeCategorie(corps []byte, s *Settings) uint32 {
	if len(corps) == 0 {
		return 0
	}
	in := NewStreamIn(corps, s)
	if s.StructHeader {
		_ = in.U8()
		_ = in.U32()
	}
	cat := in.U32()
	if in.Err() != nil {
		return 0
	}
	return cat
}

// refereePIDs extrait la liste des joueurs du parametre de StartRound :
//
//	Uint32 categorie · Uint32 salon · List<PID> joueurs · Uint8 mode · Uint32 evenement
//
// precede de l'entete de structure (version + longueur). On lit ce qu'il faut et on
// ignore le reste : seuls les PID nous interessent.
func refereePIDs(corps []byte, s *Settings) []uint64 {
	in := NewStreamIn(corps, s)
	if s.StructHeader {
		_ = in.U8()  // version
		_ = in.U32() // longueur
	}
	_ = in.U32() // categorie de donnees personnelles
	_ = in.U32() // salon
	n := in.U32()
	if n > 128 || in.Err() != nil {
		return nil // liste incoherente : mieux vaut ne prevenir personne que n'importe qui
	}
	out := make([]uint64, 0, n)
	for i := uint32(0); i < n; i++ {
		out = append(out, in.PID())
	}
	if in.Err() != nil {
		return nil
	}
	return out
}
