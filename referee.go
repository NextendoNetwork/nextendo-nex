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

			out := NewStreamOut(conn.Settings)
			out.U64(manche)
			fmt.Printf("[Referee] manche %d ouverte pour pid=%d (%d o de parametres)\n",
				manche, conn.PID, len(req.Body))

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

		case MethodRefereeEndRound, MethodRefereeEndRoundPartialReport, MethodRefereeEndRoundWithoutReport:
			// Fin de manche : le jeu remonte son rapport, nous n'en faisons rien pour
			// l'instant. Repondre VIDE plutot que refuser, sinon la partie ne se termine
			// pas proprement cote client.
			fmt.Printf("[Referee] fin de manche (methode %d) pid=%d\n", req.Method, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolMatchmakeReferee, req.Method, req.CallID, nil)

		default:
			fmt.Printf("[Referee] methode %d non traitee pid=%d bodyLen=%d corps=% x\n",
				req.Method, conn.PID, len(req.Body), req.Body)

			return notImplemented(conn, ProtocolMatchmakeReferee, req)
		}
	}
}
