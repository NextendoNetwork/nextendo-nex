package nex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Les statistiques de l'arbitre : ce que le jeu affiche comme bilan d'un joueur.
//
// Jusqu'ici on repondait « non implemente » a tout ce qui touchait aux statistiques, et on
// jetait les rapports de fin de manche. Le jeu ne pouvait donc afficher aucun bilan — non
// pas parce qu'il n'en demandait pas, mais parce qu'on ne lui rendait rien.
//
// Formes reprises du wiki de kinnay (Matchmake Referee Protocol, variante Eagle), ecrites
// ici de zero.

const (
	MethodRefereeGetRoundParticipants  uint32 = 6
	MethodRefereeGetNotSummarizedRound uint32 = 7
	MethodRefereeGetRound              uint32 = 8
	MethodRefereeGetStatsPrimary       uint32 = 9
	MethodRefereeGetStatsPrimaries     uint32 = 10
	MethodRefereeGetStatsAll           uint32 = 11
	MethodRefereeCreateStats           uint32 = 12
	MethodRefereeGetOrCreateStats      uint32 = 13
	MethodRefereeResetStats            uint32 = 14
)

// MatchmakeRefereeStats : le bilan d'un joueur dans une categorie.
//
// « recent » et « total » sont deux comptes distincts : le premier est cense se remettre a
// zero periodiquement, le second jamais. Nous ne remettons rien a zero pour l'instant, donc
// les deux avancent ensemble — c'est explicite ici plutot que d'avoir l'air d'un oubli.
type MatchmakeRefereeStats struct {
	UniqueID            uint64
	Category            uint32
	PID                 uint64
	RecentDisconnection uint32
	RecentViolation     uint32
	RecentMismatch      uint32
	RecentWin           uint32
	RecentLoss          uint32
	RecentDraw          uint32
	TotalDisconnect     uint32
	TotalViolation      uint32
	TotalMismatch       uint32
	TotalWin            uint32
	TotalLoss           uint32
	TotalDraw           uint32
	RatingValue         uint32
}

// Levels implements Structure.
func (s *MatchmakeRefereeStats) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U64(s.UniqueID)
			o.U32(s.Category)
			o.PID(s.PID)
			o.U32(s.RecentDisconnection)
			o.U32(s.RecentViolation)
			o.U32(s.RecentMismatch)
			o.U32(s.RecentWin)
			o.U32(s.RecentLoss)
			o.U32(s.RecentDraw)
			o.U32(s.TotalDisconnect)
			o.U32(s.TotalViolation)
			o.U32(s.TotalMismatch)
			o.U32(s.TotalWin)
			o.U32(s.TotalLoss)
			o.U32(s.TotalDraw)
			o.U32(s.RatingValue)
		},
		Load: func(i *StreamIn) {
			s.UniqueID = i.U64()
			s.Category = i.U32()
			s.PID = i.PID()
			s.RecentDisconnection = i.U32()
			s.RecentViolation = i.U32()
			s.RecentMismatch = i.U32()
			s.RecentWin = i.U32()
			s.RecentLoss = i.U32()
			s.RecentDraw = i.U32()
			s.TotalDisconnect = i.U32()
			s.TotalViolation = i.U32()
			s.TotalMismatch = i.U32()
			s.TotalWin = i.U32()
			s.TotalLoss = i.U32()
			s.TotalDraw = i.U32()
			s.RatingValue = i.U32()
		},
	}}
}

// MatchmakeRefereeStatsTarget : de qui on veut le bilan.
type MatchmakeRefereeStatsTarget struct {
	PID      uint64
	Category uint32
}

// Levels implements Structure.
func (t *MatchmakeRefereeStatsTarget) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.PID(t.PID); o.U32(t.Category) },
		Load: func(i *StreamIn) { t.PID = i.PID(); t.Category = i.U32() },
	}}
}

// MatchmakeRefereeStatsInitParam : la categorie et le rating de depart.
type MatchmakeRefereeStatsInitParam struct {
	Category           uint32
	InitialRatingValue uint32
}

// Levels implements Structure.
func (p *MatchmakeRefereeStatsInitParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.U32(p.Category); o.U32(p.InitialRatingValue) },
		Load: func(i *StreamIn) { p.Category = i.U32(); p.InitialRatingValue = i.U32() },
	}}
}

// MatchmakeRefereePersonalRoundResult : ce qu'un joueur rapporte a la fin d'une manche.
type MatchmakeRefereePersonalRoundResult struct {
	PID                     uint64
	PersonalRoundResultFlag uint32
	RoundWinLoss            uint32
	RatingValueChange       int32
	Buffer                  []byte
	ReportSummaryMode       uint8
	EventID                 uint32
}

// Levels implements Structure.
func (r *MatchmakeRefereePersonalRoundResult) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.PID(r.PID)
			o.U32(r.PersonalRoundResultFlag)
			o.U32(r.RoundWinLoss)
			o.S32(r.RatingValueChange)
			o.QBuffer(r.Buffer)
			o.U8(r.ReportSummaryMode)
			o.U32(r.EventID)
		},
		Load: func(i *StreamIn) {
			r.PID = i.PID()
			r.PersonalRoundResultFlag = i.U32()
			r.RoundWinLoss = i.U32()
			r.RatingValueChange = i.S32()
			r.Buffer = i.QBuffer()
			r.ReportSummaryMode = i.U8()
			r.EventID = i.U32()
		},
	}}
}

// MatchmakeRefereeEndRoundParam : le rapport complet d'une manche.
type MatchmakeRefereeEndRoundParam struct {
	RoundID              uint64
	PersonalRoundResults []MatchmakeRefereePersonalRoundResult
}

// Levels implements Structure.
func (p *MatchmakeRefereeEndRoundParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U64(p.RoundID)
			o.U32(uint32(len(p.PersonalRoundResults)))
			for idx := range p.PersonalRoundResults {
				o.Add(&p.PersonalRoundResults[idx])
			}
		},
		Load: func(i *StreamIn) {
			p.RoundID = i.U64()
			n := i.U32()
			if n > 128 || i.Err() != nil {
				return // liste incoherente : on n'alloue pas sur un entier venu du reseau
			}
			p.PersonalRoundResults = make([]MatchmakeRefereePersonalRoundResult, n)
			for idx := range p.PersonalRoundResults {
				i.Extract(&p.PersonalRoundResults[idx])
			}
		},
	}}
}

// --- le magasin ------------------------------------------------------------

type cleStats struct {
	PID      uint64 `json:"pid"`
	Category uint32 `json:"categorie"`
}

// MagasinStats garde les bilans. Une seule instance, posee par le serveur de jeu.
type MagasinStats struct {
	mu      sync.RWMutex
	parCle  map[cleStats]*MatchmakeRefereeStats
	modifie bool
}

var StatsArbitre = &MagasinStats{parCle: map[cleStats]*MatchmakeRefereeStats{}}

// CheminStatsArbitre : ou ecrire les bilans. Vide = pas de persistance.
var CheminStatsArbitre = os.Getenv("NEX_REFEREE_STATS_FICHIER")

func (m *MagasinStats) obtenirOuCreer(pid uint64, categorie, ratingInitial uint32) *MatchmakeRefereeStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	cle := cleStats{PID: pid, Category: categorie}
	if s := m.parCle[cle]; s != nil {
		return s
	}
	s := &MatchmakeRefereeStats{
		// UniqueID doit etre stable pour un couple joueur/categorie : on le DERIVE au lieu
		// de l'allouer, sinon un redemarrage rendrait au meme joueur un identifiant
		// different de celui qu'il a garde.
		UniqueID:    pid ^ (uint64(categorie) << 48),
		Category:    categorie,
		PID:         pid,
		RatingValue: ratingInitial,
	}
	m.parCle[cle] = s
	m.modifie = true
	return s
}

func (m *MagasinStats) lire(pid uint64, categorie uint32) MatchmakeRefereeStats {
	m.mu.RLock()
	s := m.parCle[cleStats{PID: pid, Category: categorie}]
	m.mu.RUnlock()
	if s == nil {
		return MatchmakeRefereeStats{Category: categorie, PID: pid}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *s
}

// toutesCategories rend tous les bilans d'un joueur.
func (m *MagasinStats) toutesCategories(pid uint64) []MatchmakeRefereeStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []MatchmakeRefereeStats
	for cle, s := range m.parCle {
		if cle.PID == pid {
			out = append(out, *s)
		}
	}
	return out
}

// appliquerResultat met a jour le bilan d'un joueur d'apres son rapport de manche.
//
// LE RATING EST FIABLE, LE RESULTAT NE L'EST PAS ENCORE. `ratingValueChange` est un entier
// signe dont le sens est evident et qu'on applique tel quel. `roundWinLoss`, lui, n'a pas
// de correspondance documentee : on retient la lecture la plus courante (1 victoire,
// 2 defaite, 3 nul) ET on journalise toute autre valeur, pour pouvoir la corriger par la
// mesure au lieu d'en debattre. Un bilan faux serait pire qu'un bilan absent.
func (m *MagasinStats) appliquerResultat(categorie uint32, r MatchmakeRefereePersonalRoundResult) {
	s := m.obtenirOuCreer(r.PID, categorie, 0)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.RoundWinLoss {
	case 0:
		// MESURE DU 2026-08-26, SMB35, sur des manches reelles : le jeu envoie bien un
		// rapport par joueur, avec le bon PID, mais TOUS les champs de resultat a zero —
		// flag, winLoss, variation de rating, et un tampon de zero octet.
		//
		// Zero ne veut donc pas dire « valeur inconnue » mais « rien a declarer ». On ne
		// compte rien et on ne dit rien : un avertissement par joueur et par manche
		// remplirait le journal pour signaler le fonctionnement normal.
	case 1:
		s.RecentWin++
		s.TotalWin++
	case 2:
		s.RecentLoss++
		s.TotalLoss++
	case 3:
		s.RecentDraw++
		s.TotalDraw++
	default:
		// Une valeur non nulle et non prevue, elle, merite d'etre vue : la correspondance
		// 1/2/3 n'est documentee nulle part, c'est la lecture la plus courante et rien de
		// plus. Si ce message apparait un jour, c'est qu'il faut la corriger.
		fmt.Printf("[Referee] roundWinLoss=%d inattendu pour pid=%d : bilan non compte (a mesurer)\n",
			r.RoundWinLoss, r.PID)
	}
	// Le rating ne descend pas sous zero : il est rendu en Uint32, donc un total negatif
	// repartirait a quatre milliards.
	delta := r.RatingValueChange
	if delta < 0 && uint32(-delta) > s.RatingValue {
		s.RatingValue = 0
	} else {
		s.RatingValue = uint32(int64(s.RatingValue) + int64(delta))
	}
	m.modifie = true
}

func (m *MagasinStats) reinitialiser(pid uint64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for cle := range m.parCle {
		if cle.PID == pid {
			delete(m.parCle, cle)
			n++
		}
	}
	if n > 0 {
		m.modifie = true
	}
	return n
}

// --- persistance ------------------------------------------------------------

type ligneStats struct {
	Cle    cleStats               `json:"cle"`
	Valeur *MatchmakeRefereeStats `json:"valeur"`
}

// Charger relit les bilans. Une carte a cle composite ne se serialise pas en JSON : on
// ecrit donc une LISTE de couples, pas une carte.
func (m *MagasinStats) Charger() {
	if CheminStatsArbitre == "" {
		return
	}
	b, err := os.ReadFile(CheminStatsArbitre)
	if err != nil {
		return
	}
	var lignes []ligneStats
	if json.Unmarshal(b, &lignes) != nil {
		fmt.Printf("[Referee] fichier de bilans illisible : %s\n", CheminStatsArbitre)
		return
	}
	m.mu.Lock()
	for _, l := range lignes {
		if l.Valeur != nil {
			m.parCle[l.Cle] = l.Valeur
		}
	}
	n := len(m.parCle)
	m.mu.Unlock()
	fmt.Printf("[Referee] bilans relus : %d entrees\n", n)
}

// Ecrire enregistre les bilans s'ils ont change.
func (m *MagasinStats) Ecrire() {
	if CheminStatsArbitre == "" {
		return
	}
	m.mu.Lock()
	if !m.modifie {
		m.mu.Unlock()
		return
	}
	lignes := make([]ligneStats, 0, len(m.parCle))
	for cle, s := range m.parCle {
		lignes = append(lignes, ligneStats{Cle: cle, Valeur: s})
	}
	m.modifie = false
	m.mu.Unlock()

	b, err := json.Marshal(lignes)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(CheminStatsArbitre), 0o755); err != nil {
		return
	}
	tmp := CheminStatsArbitre + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, CheminStatsArbitre)
	}
}
