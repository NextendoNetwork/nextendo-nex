package nex

// Classement des tournois Mario Kart 8 Deluxe.
//
// LES DEUX FORMATS, relevés sur le serveur de Nintendo (capture du 2026-08-12,
// un joueur courant un tournoi jusqu'au bout puis ouvrant le classement).
//
// Ce que le jeu DÉPOSE en fin de course (0x70/0x11, 161 octets) :
//
//	+0x00  u8   version
//	+0x01  u32  longueur du contenu
//	+0x05  u32  identifiant du TOURNOI
//	+0x09  u32  catégorie (393 sur la capture — le mode/circuit)
//	+0x11  u32  SCORE
//	+0x1b  u16  132, puis le profil du joueur (Mii + pseudo)
//
// Ce qu'il ATTEND pour afficher un classement (0x70/0x10) : des PAGES, chacune
// u32 catégorie + u32 nombre + N lignes de 164 octets :
//
//	u8 0, u32 159, u32 RANG, u64 pid, u32 score, u64 date, u8 0xff,
//	u16 132, profil[132]
//
// Les deux bouts se rejoignent : le dépôt porte déjà le profil, qui occupe 132
// des 164 octets d'une ligne. Le serveur n'a donc qu'à trier par score et poser
// les rangs. C'est le seul endroit du protocole MK8 où « stocker et rediffuser »
// ne suffisait pas — il fallait vraiment convertir.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// scoreEntryLen : taille d'une ligne de classement, vérifiée sur la capture.
	scoreEntryLen = 164
	// scoreEntryBody : longueur annoncée dans la ligne (164 - 1 version - 4 longueur).
	scoreEntryBody = 159
	// scoreProfileLen : le profil embarqué dans chaque ligne.
	scoreProfileLen = 132
	// scoreMaxPerBoard borne un classement. Nintendo sert 5 pages de 20.
	scoreMaxPerBoard = 100
)

// boardEntry est le résultat d'un joueur dans un tournoi.
type boardEntry struct {
	PID     uint64
	Score   uint32
	Profile []byte // 132 octets, tels que le joueur les a déposés
	At      time.Time
}

// boardKey identifie un classement : un tournoi, une catégorie.
type boardKey struct {
	Tournament uint32
	Category   uint32
}

var (
	boardsMu sync.RWMutex
	boards   = map[boardKey]map[uint64]*boardEntry{}
)

// parseScoreSubmission lit un dépôt de fin de course. Rend false si la trame
// n'a pas la forme attendue — on préfère ignorer un dépôt que polluer un
// classement avec des valeurs inventées.
func parseScoreSubmission(body []byte) (tournament, category, score uint32, profile []byte, ok bool) {
	le32 := func(o int) uint32 {
		return uint32(body[o]) | uint32(body[o+1])<<8 | uint32(body[o+2])<<16 | uint32(body[o+3])<<24
	}
	if len(body) < 0x1d+scoreProfileLen {
		return 0, 0, 0, nil, false
	}
	tournament = le32(0x05)
	category = le32(0x09)
	score = le32(0x11)
	// Le profil est précédé de sa longueur sur 16 bits, qui doit valoir 132.
	if int(uint16(body[0x1b])|uint16(body[0x1c])<<8) != scoreProfileLen {
		return 0, 0, 0, nil, false
	}
	profile = body[0x1d : 0x1d+scoreProfileLen]
	if tournament == 0 {
		return 0, 0, 0, nil, false
	}
	return tournament, category, score, profile, true
}

// recordScore enregistre le résultat d'un joueur. On ne garde que le MEILLEUR :
// un classement montre la performance d'un joueur, pas la dernière en date.
func recordScore(pid uint64, body []byte) bool {
	boardInit()
	tour, cat, score, profile, ok := parseScoreSubmission(body)
	if !ok {
		return false
	}
	cp := make([]byte, len(profile))
	copy(cp, profile)

	k := boardKey{Tournament: tour, Category: cat}
	boardsMu.Lock()
	defer boardsMu.Unlock()
	if boards[k] == nil {
		boards[k] = map[uint64]*boardEntry{}
	}
	prev := boards[k][pid]
	if prev != nil && prev.Score >= score {
		prev.Profile = cp // le profil peut avoir changé (pseudo, Mii)
		boardDirty.Store(true)
		return true
	}
	boards[k][pid] = &boardEntry{PID: pid, Score: score, Profile: cp, At: time.Now()}
	boardDirty.Store(true)
	fmt.Printf("[Classement] tournoi=%d catégorie=%d pid=%d score=%d\n", tour, cat, pid, score)
	return true
}

// boardFor rend les résultats d'un tournoi, triés par score décroissant. À score
// égal, le plus ancien passe devant : arriver premier à un score doit récompenser.
func boardFor(tour, cat uint32) []*boardEntry {
	boardInit()
	boardsMu.RLock()
	m := boards[boardKey{Tournament: tour, Category: cat}]
	out := make([]*boardEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	boardsMu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].At.Before(out[j].At)
	})
	if len(out) > scoreMaxPerBoard {
		out = out[:scoreMaxPerBoard]
	}
	return out
}

// writeBoard sérialise un classement au format attendu par le jeu.
func writeBoard(cat uint32, entries []*boardEntry) []byte {
	var b []byte
	put16 := func(v uint16) { b = append(b, byte(v), byte(v>>8)) }
	put32 := func(v uint32) { b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	put64 := func(v uint64) {
		for i := 0; i < 8; i++ {
			b = append(b, byte(v>>(8*i)))
		}
	}

	// La réponse est une LISTE de pages ; on en produit une seule, qui suffit
	// jusqu'à 100 joueurs.
	put32(1) // une page

	var page []byte
	pput16 := func(v uint16) { page = append(page, byte(v), byte(v>>8)) }
	pput32 := func(v uint32) { page = append(page, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	pput64 := func(v uint64) {
		for i := 0; i < 8; i++ {
			page = append(page, byte(v>>(8*i)))
		}
	}
	pput32(cat)
	pput32(uint32(len(entries)))
	for i, e := range entries {
		page = append(page, 0) // version
		pput32(scoreEntryBody)
		pput32(uint32(i + 1)) // RANG, à partir de 1
		pput64(e.PID)
		pput32(e.Score)
		pput64(nexDateTime(e.At))
		page = append(page, 0xff)
		pput16(scoreProfileLen)
		prof := e.Profile
		if len(prof) != scoreProfileLen { // ceinture : une ligne doit faire 164 octets
			fixed := make([]byte, scoreProfileLen)
			copy(fixed, prof)
			prof = fixed
		}
		page = append(page, prof...)
	}

	// PIED DE PAGE, 24 octets. Il manquait, et sans lui le jeu refuse la page
	// entière : « Pas de classement disponible pour l'instant » alors que le
	// serveur renvoyait bien huit joueurs. Relevé sur les dix pages de la
	// capture : le nombre TOTAL de joueurs de la manche, puis 4 (constant sur
	// toutes), puis seize zéros.
	pput32(uint32(len(entries)))
	pput32(4)
	page = append(page, make([]byte, 16)...)

	b = append(b, 0) // version de la page
	put32(uint32(len(page)))
	b = append(b, page...)
	_ = put16
	_ = put64
	return b
}

// nexDateTime encode un instant au format compressé de NEX : seconde, minute,
// heure, jour, mois, année empilés dans un entier 64 bits.
func nexDateTime(t time.Time) uint64 {
	t = t.UTC()
	return uint64(t.Second()) |
		uint64(t.Minute())<<6 |
		uint64(t.Hour())<<12 |
		uint64(t.Day())<<17 |
		uint64(t.Month())<<22 |
		uint64(t.Year())<<26
}

// competitionRanking répond à la demande de classement. La requête porte une
// version, une longueur, puis l'identifiant du tournoi.
func competitionRanking(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	empty := func() *RMCMessage {
		out := NewStreamOut(s)
		out.U32(0) // aucune page : la forme que Nintendo rend sur un tournoi vide
		return NewRMCSuccess(s, ProtocolRanking, req.Method, req.CallID, out.Bytes())
	}
	if len(req.Body) < 13 {
		return empty()
	}
	le32 := func(o int) uint32 {
		return uint32(req.Body[o]) | uint32(req.Body[o+1])<<8 | uint32(req.Body[o+2])<<16 | uint32(req.Body[o+3])<<24
	}
	tour := le32(5)
	entries := boardFor(tour, boardCategoryFor(tour))
	if len(entries) == 0 {
		return empty()
	}
	fmt.Printf("[Classement] tournoi=%d -> %d joueur(s)\n", tour, len(entries))
	return NewRMCSuccess(s, ProtocolRanking, req.Method, req.CallID,
		writeBoard(boardCategoryFor(tour), entries))
}

// boardCategoryFor rend la catégorie enregistrée pour ce tournoi. Le jeu ne la
// répète pas dans sa demande de classement : on retient celle de ses dépôts.
func boardCategoryFor(tour uint32) uint32 {
	boardsMu.RLock()
	defer boardsMu.RUnlock()
	for k := range boards {
		if k.Tournament == tour {
			return k.Category
		}
	}
	return 0
}

// --- persistance ------------------------------------------------------------
//
// Les scores DOIVENT survivre à un redémarrage : un tournoi dure plusieurs
// jours, et un classement effacé par une mise à jour du serveur ne se
// reconstitue qu'en refaisant courir tout le monde. Constaté en direct — un
// déploiement a effacé le classement de dix joueurs.

type boardPersisted struct {
	Tournament uint32 `json:"tournament"`
	Category   uint32 `json:"category"`
	PID        uint64 `json:"pid"`
	Score      uint32 `json:"score"`
	Profile    []byte `json:"profile"`
	At         int64  `json:"at"`
}

var (
	boardFile  = os.Getenv("NEXTENDO_BOARD_FILE")
	boardOnce  sync.Once
	boardDirty atomic.Bool
)

func boardInit() {
	boardOnce.Do(func() {
		if boardFile == "" {
			return
		}
		if b, err := os.ReadFile(boardFile); err == nil {
			var list []boardPersisted
			if json.Unmarshal(b, &list) == nil {
				boardsMu.Lock()
				for _, p := range list {
					if len(p.Profile) != scoreProfileLen || p.Tournament == 0 {
						continue
					}
					k := boardKey{p.Tournament, p.Category}
					if boards[k] == nil {
						boards[k] = map[uint64]*boardEntry{}
					}
					boards[k][p.PID] = &boardEntry{
						PID: p.PID, Score: p.Score, Profile: p.Profile, At: time.Unix(p.At, 0),
					}
				}
				n := 0
				for _, m := range boards {
					n += len(m)
				}
				boardsMu.Unlock()
				fmt.Printf("[Classement] %d score(s) restauré(s) depuis %s\n", n, boardFile)
			}
		}
		go boardFlusher()
	})
}

func boardFlusher() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if !boardDirty.Swap(false) {
			continue
		}
		boardsMu.RLock()
		var list []boardPersisted
		for k, m := range boards {
			for pid, e := range m {
				list = append(list, boardPersisted{
					Tournament: k.Tournament, Category: k.Category,
					PID: pid, Score: e.Score, Profile: e.Profile, At: e.At.Unix(),
				})
			}
		}
		boardsMu.RUnlock()
		b, err := json.Marshal(list)
		if err != nil {
			continue
		}
		tmp := boardFile + ".tmp"
		if os.WriteFile(tmp, b, 0644) == nil {
			_ = os.Rename(tmp, boardFile)
		}
	}
}
