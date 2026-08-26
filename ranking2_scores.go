package nex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Les structures de Ranking2, et le classement qui va avec.
//
// POURQUOI CE FICHIER EXISTE. Le classement rendait une liste vide parce qu'on n'avait
// rien a mettre dedans : PutScore, la methode par laquelle le jeu DEPOSE un score, avait
// une constante mais aucun traitement — elle tombait dans le cas par defaut et repondait
// « non implemente ». Le score n'entrait donc jamais. Ce n'etait pas un probleme de
// classement, c'etait un probleme de collecte.
//
// Les formes ci-dessous viennent du wiki de kinnay (Ranking Protocol 2). Lues pour les
// faits du protocole, ecrites ici de zero : rien n'est recopie d'un depot AGPL.

// Ranking2CommonData : ce que le joueur montre a cote de son score.
type Ranking2CommonData struct {
	UserName   string
	Mii        []byte
	BinaryData []byte
}

// Levels implements Structure.
func (d *Ranking2CommonData) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.String(d.UserName)
			o.QBuffer(d.Mii)
			o.QBuffer(d.BinaryData)
		},
		Load: func(i *StreamIn) {
			d.UserName = i.String()
			d.Mii = i.QBuffer()
			d.BinaryData = i.QBuffer()
		},
	}}
}

// Ranking2ScoreData : un score depose.
//
// Misc n'a pas de sens documente. On le CONSERVE et on le rend tel quel : c'est le jeu qui
// l'a ecrit, lui seul sait ce qu'il y met, et le perdre serait une facon silencieuse de
// corrompre l'entree.
type Ranking2ScoreData struct {
	Misc     uint64
	Category uint32
	Score    uint32
}

// Levels implements Structure.
func (d *Ranking2ScoreData) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.U64(d.Misc); o.U32(d.Category); o.U32(d.Score) },
		Load: func(i *StreamIn) { d.Misc = i.U64(); d.Category = i.U32(); d.Score = i.U32() },
	}}
}

// Ranking2RankData : une ligne du classement.
type Ranking2RankData struct {
	Misc        uint64
	NexUniqueID uint64
	PrincipalID uint64
	Rank        uint32
	Score       uint32
	CommonData  Ranking2CommonData
}

// Levels implements Structure.
func (d *Ranking2RankData) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U64(d.Misc)
			o.U64(d.NexUniqueID)
			o.PID(d.PrincipalID)
			o.U32(d.Rank)
			o.U32(d.Score)
			o.Add(&d.CommonData)
		},
		Load: func(i *StreamIn) {
			d.Misc = i.U64()
			d.NexUniqueID = i.U64()
			d.PrincipalID = i.PID()
			d.Rank = i.U32()
			d.Score = i.U32()
			i.Extract(&d.CommonData)
		},
	}}
}

// Ranking2Info : la reponse de GetRanking.
//
// On n'ecrivait qu'un u32 a zero, en croyant rendre « une liste vide ». Il manquait TROIS
// champs derriere — le rang le plus bas, le nombre de classes, la saison. Avec une liste
// reellement vide le client s'en accommodait ; des la premiere entree il aurait lu douze
// octets de travers.
type Ranking2Info struct {
	Data        []Ranking2RankData
	LowestRank  uint32
	NumRankedIn uint32
	Season      int32
}

// Levels implements Structure.
func (r *Ranking2Info) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U32(uint32(len(r.Data)))
			for idx := range r.Data {
				o.Add(&r.Data[idx])
			}
			o.U32(r.LowestRank)
			o.U32(r.NumRankedIn)
			o.S32(r.Season)
		},
		Load: func(i *StreamIn) {
			r.Data = make([]Ranking2RankData, i.U32())
			for idx := range r.Data {
				i.Extract(&r.Data[idx])
			}
			r.LowestRank = i.U32()
			r.NumRankedIn = i.U32()
			r.Season = i.S32()
		},
	}}
}

// Ranking2GetParam : ce que demande GetRanking.
type Ranking2GetParam struct {
	NexUniqueID        uint64
	PrincipalID        uint64
	Category           uint32
	Offset             uint32
	Length             uint32
	SortFlags          uint32
	OptionFlags        uint32
	Mode               uint8
	NumSeasonsToGoBack uint8
}

// Levels implements Structure.
func (p *Ranking2GetParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U64(p.NexUniqueID)
			o.PID(p.PrincipalID)
			o.U32(p.Category)
			o.U32(p.Offset)
			o.U32(p.Length)
			o.U32(p.SortFlags)
			o.U32(p.OptionFlags)
			o.U8(p.Mode)
			o.U8(p.NumSeasonsToGoBack)
		},
		Load: func(i *StreamIn) {
			p.NexUniqueID = i.U64()
			p.PrincipalID = i.PID()
			p.Category = i.U32()
			p.Offset = i.U32()
			p.Length = i.U32()
			p.SortFlags = i.U32()
			p.OptionFlags = i.U32()
			p.Mode = i.U8()
			p.NumSeasonsToGoBack = i.U8()
		},
	}}
}

// Ranking2EstimateScoreRankOutput : la reponse du rang estime.
//
// On en rendait DEUX champs sur six. Le client lisait donc treize octets qui ne lui
// etaient pas destines.
type Ranking2EstimateScoreRankOutput struct {
	Rank         uint32
	Length       uint32
	Score        uint32
	Category     uint32
	Season       int32
	SamplingRate uint8
}

// Levels implements Structure.
func (o2 *Ranking2EstimateScoreRankOutput) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U32(o2.Rank)
			o.U32(o2.Length)
			o.U32(o2.Score)
			o.U32(o2.Category)
			o.S32(o2.Season)
			o.U8(o2.SamplingRate)
		},
		Load: func(i *StreamIn) {
			o2.Rank = i.U32()
			o2.Length = i.U32()
			o2.Score = i.U32()
			o2.Category = i.U32()
			o2.Season = i.S32()
			o2.SamplingRate = i.U8()
		},
	}}
}

// --- le classement lui-meme -------------------------------------------------

// entreeClassement : le meilleur score d'un joueur dans une categorie.
type entreeClassement struct {
	PID         uint64 `json:"pid"`
	NexUniqueID uint64 `json:"nex_unique_id"`
	Misc        uint64 `json:"misc"`
	Score       uint32 `json:"score"`
}

// deposer garde le MEILLEUR score, pas le dernier.
//
// Un classement qui retient le dernier score fait reculer un joueur quand il refait une
// partie moins bonne — ce n'est pas ce que « classement » veut dire, et c'est ce qu'on
// aurait obtenu en ecrasant sans comparer.
func (r *Ranking2Store) deposer(categorie uint32, e entreeClassement) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.scores == nil {
		r.scores = map[uint32]map[uint64]entreeClassement{}
	}
	cat := r.scores[categorie]
	if cat == nil {
		cat = map[uint64]entreeClassement{}
		r.scores[categorie] = cat
	}
	ancien, existe := cat[e.PID]
	if existe && ancien.Score >= e.Score {
		return false
	}
	cat[e.PID] = e
	r.modifie = true
	return true
}

// classement rend une categorie triee, du meilleur au moins bon.
//
// L'egalite se departage par le PID. Sans cette seconde cle, deux scores identiques
// changeraient d'ordre a chaque appel — l'ordre de parcours d'une carte Go est
// volontairement aleatoire — et le classement sauterait sous les yeux du joueur.
func (r *Ranking2Store) classement(categorie uint32) []entreeClassement {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cat := r.scores[categorie]
	out := make([]entreeClassement, 0, len(cat))
	for _, e := range cat {
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].PID < out[b].PID
	})
	return out
}

// donneesCommunes rend le bloc de presentation d'un joueur, deja decode.
func (r *Ranking2Store) donneesCommunes(pid uint64, s *Settings) Ranking2CommonData {
	r.mu.RLock()
	brut := r.communs[pid]
	r.mu.RUnlock()
	var d Ranking2CommonData
	if len(brut) == 0 {
		return d
	}
	// Le corps depose par PutCommonData est « la structure PUIS un u64 ». On ne lit que la
	// structure : le reste ne nous regarde pas ici.
	in := NewStreamIn(brut, s)
	in.Extract(&d)
	if in.Err() != nil {
		return Ranking2CommonData{}
	}
	return d
}

// --- persistance ------------------------------------------------------------
//
// Le commentaire d'origine disait qu'un classement perdu au redemarrage etait « un
// desagrement, pas une perte de donnee de compte ». C'etait vrai tant qu'il etait vide.
// Un classement qui repart de zero a chaque deploiement n'est pas un classement : autant
// ne pas en afficher. On l'ecrit donc sur disque, dans un fichier a part — jamais dans
// accounts.json, qui est deja le point faible de la maison.

// CheminClassement : ou ecrire. Vide = pas de persistance (comportement d'avant).
var CheminClassement = os.Getenv("NEX_RANKING2_FICHIER")

type fichierClassement struct {
	Scores map[uint32]map[uint64]entreeClassement `json:"scores"`
}

// Charger relit le classement. Silencieux si le fichier n'existe pas encore.
func (r *Ranking2Store) Charger() {
	if CheminClassement == "" {
		return
	}
	b, err := os.ReadFile(CheminClassement)
	if err != nil {
		return
	}
	var f fichierClassement
	if json.Unmarshal(b, &f) != nil || f.Scores == nil {
		fmt.Printf("[Ranking2] fichier de classement illisible : %s\n", CheminClassement)
		return
	}
	r.mu.Lock()
	r.scores = f.Scores
	r.mu.Unlock()
	var n int
	for _, cat := range f.Scores {
		n += len(cat)
	}
	fmt.Printf("[Ranking2] classement relu : %d categories, %d entrees\n", len(f.Scores), n)
}

// Ecrire enregistre le classement s'il a change.
//
// Ecriture par fichier temporaire puis renommage : une coupure au mauvais moment laisse
// l'ancien fichier intact plutot qu'un JSON tronque, qui serait illisible au demarrage
// suivant et perdrait TOUT au lieu des dernieres secondes.
func (r *Ranking2Store) Ecrire() {
	if CheminClassement == "" {
		return
	}
	r.mu.Lock()
	if !r.modifie {
		r.mu.Unlock()
		return
	}
	b, err := json.Marshal(fichierClassement{Scores: r.scores})
	r.modifie = false
	r.mu.Unlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(CheminClassement), 0o755); err != nil {
		return
	}
	tmp := CheminClassement + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, CheminClassement)
	}
}
