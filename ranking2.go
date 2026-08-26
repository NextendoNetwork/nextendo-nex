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

	MethodRanking2PutScore           uint32 = 1
	MethodRanking2GetCommonData      uint32 = 2
	MethodRanking2PutCommonData      uint32 = 3
	MethodRanking2DelCommonData      uint32 = 4
	MethodRanking2GetCategorySetting uint32 = 7
)

// Ranking2CategorySetting decrit une categorie de classement : les bornes de score, le
// rang le plus bas retenu, et quand la saison se remet a zero.
type Ranking2CategorySetting struct {
	MinScore           uint32
	MaxScore           uint32
	LowestRank         uint32
	ResetMonth         uint16
	ResetDay           uint8
	ResetHour          uint8
	ResetMode          uint8
	MaxSeasonsToGoBack uint8
	ScoreOrder         bool
}

// Levels implements Structure.
func (s *Ranking2CategorySetting) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U32(s.MinScore)
			o.U32(s.MaxScore)
			o.U32(s.LowestRank)
			o.U16(s.ResetMonth)
			o.U8(s.ResetDay)
			o.U8(s.ResetHour)
			o.U8(s.ResetMode)
			o.U8(s.MaxSeasonsToGoBack)
			o.Bool(s.ScoreOrder)
		},
		Load: func(i *StreamIn) {
			s.MinScore = i.U32()
			s.MaxScore = i.U32()
			s.LowestRank = i.U32()
			s.ResetMonth = i.U16()
			s.ResetDay = i.U8()
			s.ResetHour = i.U8()
			s.ResetMode = i.U8()
			s.MaxSeasonsToGoBack = i.U8()
			s.ScoreOrder = i.Bool()
		},
	}}
}

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

		case MethodRanking2GetCategorySetting:
			// Le corps ne porte que le numero de categorie. On rend une categorie SANS
			// remise a zero et sans borne de rang : nous ne tenons pas encore de
			// classement, et une saison qui expire ferait disparaitre des scores qu'on
			// n'a de toute facon pas. Le jeu, lui, a juste besoin d'une reponse valide
			// pour continuer — sans elle il s'arrete avant meme de rejoindre le relais.
			cat := uint32(0)
			if len(req.Body) >= 4 {
				cat = NewStreamIn(req.Body, conn.Settings).U32()
			}
			out := NewStreamOut(conn.Settings)
			out.Add(&Ranking2CategorySetting{
				MinScore:   0,
				MaxScore:   0xFFFFFFFF,
				LowestRank: 0, // 0 = pas de limite
				ScoreOrder: true,
			})
			fmt.Printf("[Ranking2] reglages de la categorie %d rendus a pid=%d\n", cat, conn.PID)

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, out.Bytes())

		default:
			fmt.Printf("[Ranking2] methode %d non traitee pid=%d bodyLen=%d corps=% x\n",
				req.Method, conn.PID, len(req.Body), req.Body)

			return notImplemented(conn, ProtocolRanking2, req)
		}
	}
}
