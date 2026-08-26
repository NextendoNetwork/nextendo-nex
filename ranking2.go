package nex

import (
	"fmt"
	"sync"
)

// Ranking2 (protocole 122) — les classements des titres « 99 ».
//
// SUPER MARIO BROS. 35 s'en sert des la connexion : il depose ses « donnees communes »,
// un bloc opaque ou voyage entre autres le pseudo du joueur. Sans reponse il n'avance pas.
//
// Le serveur ne LIT pas ce bloc : il le garde tel quel et le rend a l'identique. C'est
// suffisant, et ca evite d'inventer une structure qu'on ne connait pas.
const (
	ProtocolRanking2 uint16 = 0x7A

	MethodRanking2PutScore      uint32 = 1
	MethodRanking2GetCommonData uint32 = 2
	MethodRanking2PutCommonData uint32 = 3
	MethodRanking2DelCommonData uint32 = 4
)

// Ranking2Store garde les donnees communes par joueur. En memoire : un classement perdu
// au redemarrage est un desagrement, pas une perte de donnee de compte.
type Ranking2Store struct {
	mu      sync.RWMutex
	communs map[uint64][]byte
}

func NewRanking2Store() *Ranking2Store {
	return &Ranking2Store{communs: map[uint64][]byte{}}
}

// Handler rend le gestionnaire du protocole 122.
func (r *Ranking2Store) Handler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodRanking2PutCommonData:
			// Le corps porte la structure PUIS un u64 d'identifiant. On conserve
			// l'ensemble sans l'interpreter : ce qu'on rendra doit etre ce qu'on a recu.
			r.mu.Lock()
			r.communs[conn.PID] = append([]byte(nil), req.Body...)
			r.mu.Unlock()
			fmt.Printf("[Ranking2] donnees communes deposees pid=%d (%d o)\n", conn.PID, len(req.Body))

			// REPONSE VIDE. Le client refuse le moindre octet en trop — il verifie qu'il a
			// tout lu et leve une erreur sinon.
			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, nil)

		case MethodRanking2GetCommonData:
			r.mu.RLock()
			data := r.communs[conn.PID]
			r.mu.RUnlock()
			fmt.Printf("[Ranking2] donnees communes rendues pid=%d (%d o)\n", conn.PID, len(data))

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, data)

		case MethodRanking2DelCommonData:
			r.mu.Lock()
			delete(r.communs, conn.PID)
			r.mu.Unlock()

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, nil)

		default:
			fmt.Printf("[Ranking2] methode %d non traitee pid=%d bodyLen=%d corps=% x\n",
				req.Method, conn.PID, len(req.Body), req.Body)

			return notImplemented(conn, ProtocolRanking2, req)
		}
	}
}
