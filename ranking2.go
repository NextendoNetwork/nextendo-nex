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
	MethodRanking2GetRanking         uint32 = 5
	MethodRanking2GetCategorySetting uint32 = 7
	MethodRanking2GetEstimateMyRank  uint32 = 11
)

// ranking2LowestRank : le rang le plus bas retenu par une categorie. Une seule
// definition pour GetRanking et GetCategorySetting — les voir diverger est ce qui
// rendrait la reponse incoherente.
const ranking2LowestRank uint32 = 10000

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

// Ranking2Store garde les donnees communes par joueur et le classement par categorie.
//
// Les donnees communes restent en memoire : le jeu les redepose a chaque connexion, donc
// les perdre ne coute rien. Les scores, eux, sont ecrits sur disque (voir
// ranking2_scores.go) — un classement qui repart de zero a chaque deploiement n'en est pas
// un.
type Ranking2Store struct {
	mu      sync.RWMutex
	communs map[uint64][]byte
	scores  map[uint32]map[uint64]entreeClassement
	modifie bool
}

func NewRanking2Store() *Ranking2Store {
	s := &Ranking2Store{
		communs: map[uint64][]byte{},
		scores:  map[uint32]map[uint64]entreeClassement{},
	}
	s.Charger()
	return s
}

// Handler rend le gestionnaire du protocole 122.
func (r *Ranking2Store) Handler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		switch req.Method {
		case MethodRanking2PutScore:
			// Le corps est « liste de Ranking2ScoreData PUIS un u64 d'identifiant unique ».
			//
			// C'est LA methode qui manquait. Sans elle le score n'entrait jamais, et le
			// classement ne pouvait etre que vide quoi qu'on fasse en aval.
			in := NewStreamIn(req.Body, conn.Settings)
			n := in.U32()
			var retenus, deposes int
			var uid uint64
			// Borne de securite : le compte vient du reseau. Sans elle, un entier aberrant
			// ferait boucler des milliards de fois sur un corps de quelques octets.
			if n > 256 {
				fmt.Printf("[Ranking2] PutScore : compte aberrant (%d) pid=%d, ignore\n", n, conn.PID)
				return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, nil)
			}
			scores := make([]Ranking2ScoreData, n)
			for idx := range scores {
				in.Extract(&scores[idx])
			}
			if in.Remaining() >= 8 {
				uid = in.U64()
			}
			if in.Err() != nil {
				fmt.Printf("[Ranking2] PutScore illisible pid=%d : %v (corps=% x)\n", conn.PID, in.Err(), req.Body)
				return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, nil)
			}
			for _, sd := range scores {
				deposes++
				if r.deposer(sd.Category, entreeClassement{
					PID:         conn.PID,
					NexUniqueID: uid,
					Misc:        sd.Misc,
					Score:       sd.Score,
				}) {
					retenus++
				}
			}
			r.Ecrire()
			fmt.Printf("[Ranking2] PutScore pid=%d uid=%d : %d score(s), %d retenu(s) comme meilleur\n",
				conn.PID, uid, deposes, retenus)

			// Reponse VIDE, comme PutCommonData : le client verifie qu'il a tout lu.
			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, nil)

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
			// TROIS FAUTES ICI, et la troisieme provoquait 2306-0116 (Core::BufferOverflow).
			//
			//  1. On IGNORAIT le joueur demande. La requete porte
			//     « optionFlags u32 · principalId PID · nexUniqueId u64 » : le jeu demande
			//     les donnees d'UN joueur precis — celles des autres, pour afficher leurs
			//     noms dans le classement — et on rendait toujours celles de l'appelant.
			//
			//  2. On rendait le corps BRUT depose par PutCommonData, qui vaut « la structure
			//     PUIS un u64 ». Huit octets de trop, que le client compte et refuse.
			//
			//  3. Et quand on n'avait rien pour ce joueur, on rendait ZERO octet. Le client
			//     essaie alors d'extraire une structure du vide et deborde. Une structure
			//     VIDE et une reponse vide ne sont pas la meme chose.
			in := NewStreamIn(req.Body, conn.Settings)
			_ = in.U32() // optionFlags
			cible := in.PID()
			if in.Err() != nil || cible == 0 {
				cible = conn.PID
			}
			d := r.donneesCommunes(cible, conn.Settings)
			out := NewStreamOut(conn.Settings)
			out.Add(&d)
			fmt.Printf("[Ranking2] donnees communes de pid=%d rendues a pid=%d (nom=%q, %d o)\n",
				cible, conn.PID, d.UserName, len(out.Bytes()))

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, out.Bytes())

		case MethodRanking2DelCommonData:
			r.mu.Lock()
			delete(r.communs, conn.PID)
			r.mu.Unlock()

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, nil)

		case MethodRanking2GetRanking:
			var p Ranking2GetParam
			NewStreamIn(req.Body, conn.Settings).Extract(&p)

			tous := r.classement(p.Category)

			// La fenetre demandee. Length vaut zero quand le jeu ne precise rien : on rend
			// alors tout, plutot qu'une page vide qui ressemblerait a « aucun score ».
			debut := int(p.Offset)
			if debut > len(tous) {
				debut = len(tous)
			}
			fin := len(tous)
			if p.Length > 0 && debut+int(p.Length) < fin {
				fin = debut + int(p.Length)
			}

			info := Ranking2Info{
				// lowestRank est le rang le plus bas que la CATEGORIE retient — un
				// plafond de configuration, pas un compte. Je l'avais confondu avec le
				// nombre d'inscrits, ce qui donnait zero sur un classement vide : le jeu
				// redemandait alors le classement en boucle sans jamais l'afficher.
				//
				// Le serveur de reference (kinnay, lu pour les faits) rend 10000 meme
				// vide, et la meme valeur dans GetCategorySetting. Les deux doivent
				// s'accorder : elles decrivent la meme categorie.
				LowestRank:  ranking2LowestRank,
				NumRankedIn: uint32(len(tous)),
			}
			for idx := debut; idx < fin; idx++ {
				e := tous[idx]
				info.Data = append(info.Data, Ranking2RankData{
					Misc:        e.Misc,
					NexUniqueID: e.NexUniqueID,
					PrincipalID: e.PID,
					Rank:        uint32(idx + 1), // le rang commence a 1
					Score:       e.Score,
					CommonData:  r.donneesCommunes(e.PID, conn.Settings),
				})
			}

			out := NewStreamOut(conn.Settings)
			out.Add(&info)
			fmt.Printf("[Ranking2] classement categorie=%d rendu a pid=%d : %d/%d entrees\n",
				p.Category, conn.PID, len(info.Data), len(tous))

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, out.Bytes())

		case MethodRanking2GetEstimateMyRank:
			// Variante Eagle : l'entree est « categorie + nombre de saisons a remonter »,
			// SANS score — le serveur va chercher celui du joueur lui-meme.
			in := NewStreamIn(req.Body, conn.Settings)
			cat := in.U32()

			tous := r.classement(cat)
			rang := uint32(len(tous) + 1) // non classe : juste derriere le dernier
			var score uint32
			for idx, e := range tous {
				if e.PID == conn.PID {
					rang = uint32(idx + 1)
					score = e.Score
					break
				}
			}

			out := NewStreamOut(conn.Settings)
			out.Add(&Ranking2EstimateScoreRankOutput{
				Rank:     rang,
				Length:   uint32(len(tous)),
				Score:    score,
				Category: cat,
				// Echantillonnage a 1 : notre rang est EXACT, il est calcule sur la liste
				// entiere. Le vrai service estime sur un echantillon parce qu'il classe des
				// millions de joueurs ; nous, non.
				SamplingRate: 1,
			})
			fmt.Printf("[Ranking2] rang estime pid=%d categorie=%d : %d/%d (score %d)\n",
				conn.PID, cat, rang, len(tous), score)

			return NewRMCSuccess(conn.Settings, ProtocolRanking2, req.Method, req.CallID, out.Bytes())

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
			// VALEURS ALIGNEES SUR LE SERVEUR DE REFERENCE (kinnay, lu pour les faits, rien
			// de recopie). Les precedentes etaient de mon invention et deux d'entre elles
			// etaient douteuses : un maximum a 0xFFFFFFFF, soit un score impossible, et
			// surtout un lowestRank a zero — que j'avais annote « 0 = pas de limite » sans
			// rien pour l'etayer. Zero est tout aussi lisible comme « aucun rang retenu ».
			//
			// resetMonth vaut 4095 (0xFFF) : un mois qui n'existe pas, donc une saison qui
			// n'arrive jamais a terme. C'est ce qu'on veut — nous ne gerons pas de saisons,
			// et une remise a zero effacerait un classement qu'on ne saurait pas reconstruire.
			out := NewStreamOut(conn.Settings)
			out.Add(&Ranking2CategorySetting{
				MinScore:   0,
				MaxScore:   999999999,
				LowestRank: ranking2LowestRank,
				ResetMonth: 4095,
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
