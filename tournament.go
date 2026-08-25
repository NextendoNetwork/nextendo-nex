package nex

// Tournois Mario Kart 8 Deluxe.
//
// CE QU'ILS SONT : des compétitions créées par les JOUEURS — un nom, deux noms
// d'équipes, une douzaine de règles (cylindrée, objets, circuits), et un code à
// 12 chiffres pour les partager. La measured du serveur de Nintendo en montre 85
// en même temps : « Mario vs Luigi », « Hide & Seek », « 200CC TOURNAMENT ».
//
// LE PRINCIPE, ET IL EST SIMPLE : on ne décode PAS la structure entière. Le créateur
// envoie une structure décrivant tout son tournoi ; le serveur n'y touche qu'à
// trois endroits et rend le reste verbatim :
//
//	+0x00  identifiant   0 à l'envoi        -> attribué par le serveur
//	+0x04  marque        8 octets à zéro    -> tirée au sort (PAS le propriétaire,
//	                                          voir stampTournament)
//	 …     code          chaîne VIDE        -> 12 chiffres générés
//
// Le reste — règles, le nom, les équipes — nous est opaque et le
// restera : on localise structurellement le champ du code en parcourant le nom
// UTF-16 et la liste de propriétés de règles jusqu'au terminateur 0xFF.

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MethodTournamentCreate : le jeu envoie son tournoi, on le rend complété.
	MethodTournamentCreate uint32 = 0x3D
	// MethodTournamentDelete : suppression, l'identifiant sur 4 octets. Absente de
	// la measured (aucun tournoi n'y a été supprimé) ; relevée en production, où
	// elle arrivait non gérée et rendait donc une erreur 2306-0103 au joueur.
	MethodTournamentDelete uint32 = 0x3E
	// tournamentCodeLen : longueur du code partagé, relevée sur la measured.
	tournamentCodeLen = 12
	// tournamentMaxBlob borne ce qu'un client peut nous faire stocker (la measured
	// donne 233 octets ; on garde de la marge sans accepter n'importe quoi).
	tournamentMaxBlob = 4096
	// tournamentMax borne le nombre de tournois retenus. Nintendo en sert 85 ;
	// au-delà de cette borne on cesse d'en créer plutôt que de croître sans fin.
	tournamentMax = 2000
	// tournamentNameLengthOffset is the u16 byte length of the UTF-16 name,
	// including its terminator, in the measured MK8D structure.
	tournamentNameLengthOffset = 0x69
)

// Tournament est un tournoi tel qu'il circule : sa structure d'origine, plus ce
// que le serveur y a posé.
type Tournament struct {
	ID           uint32   `json:"id"`
	Owner        uint64   `json:"owner"`
	Code         string   `json:"code"`
	Mark         []byte   `json:"mark,omitempty"` // les 8 octets en +0x04, propres au tournoi
	Blob         []byte   `json:"blob"`           // structure complète, identifiants déjà posés
	Participants []uint64 `json:"participants,omitempty"`
	CreatedAt    int64    `json:"created_at"`
}

type tournamentStore struct {
	mu          sync.RWMutex
	byID        map[uint32]*Tournament
	quarantined []*Tournament
	nextID      uint32
}

var (
	tournaments     = &tournamentStore{byID: map[uint32]*Tournament{}, quarantined: []*Tournament{}, nextID: 1000}
	tournamentFile  = os.Getenv("NEXTENDO_TOURNAMENT_FILE")
	tournamentOnce  sync.Once
	tournamentDirty atomic.Bool
)

// tournamentInit recharge les tournois et lance l'écriture différée. Un tournoi
// vit plusieurs jours : le perdre à chaque redémarrage n'aurait aucun sens.
func tournamentInit() {
	tournamentOnce.Do(func() {
		if tournamentFile == "" {
			return
		}
		if b, err := os.ReadFile(tournamentFile); err == nil {
			var list []*Tournament
			if json.Unmarshal(b, &list) == nil {
				tournaments.mu.Lock()
				quarantinedCount := 0
				for _, t := range list {
					if t == nil || t.ID == 0 {
						continue
					}
					// Advance nextID for every recorded tournament to prevent duplicate ID reuse
					if t.ID >= tournaments.nextID {
						tournaments.nextID = t.ID + 1
					}
					if !validStoredTournament(t) {
						tournaments.quarantined = append(tournaments.quarantined, t)
						quarantinedCount++
						continue
					}
					tournaments.byID[t.ID] = t
				}
				n := len(tournaments.byID)
				tournaments.mu.Unlock()
				fmt.Printf("[Tournoi] %d tournoi(s) restauré(s) depuis %s\n", n, tournamentFile)
				if quarantinedCount != 0 {
					fmt.Printf("[Tournoi] %d tournoi(s) invalide(s) mis en quarantaine (conservés sans être diffusés)\n", quarantinedCount)
				}
			}
		}
		go tournamentFlusher()
	})
}

func tournamentFlusher() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if !tournamentDirty.Swap(false) {
			continue
		}
		tournaments.mu.RLock()
		list := make([]*Tournament, 0, len(tournaments.byID)+len(tournaments.quarantined))
		// Preserve quarantined entries so they are not wiped from disk
		list = append(list, tournaments.quarantined...)
		for _, v := range tournaments.byID {
			list = append(list, v)
		}
		tournaments.mu.RUnlock()
		b, err := json.Marshal(list)
		if err != nil {
			continue
		}
		tmp := tournamentFile + ".tmp"
		if os.WriteFile(tmp, b, 0644) == nil {
			_ = os.Rename(tmp, tournamentFile)
		}
	}
}

// newTournamentCode tire un code à 12 chiffres, comme celui que Nintendo rend.
func newTournamentCode() string {
	b := make([]byte, tournamentCodeLen)
	_, _ = rand.Read(b)
	out := make([]byte, tournamentCodeLen)
	for i, v := range b {
		out[i] = '0' + v%10
	}
	return string(out)
}

// tournamentCodeOffset locates the code field by walking the UTF-16 name length
// and the variable-length rule property list until its 0xFF terminator.
func tournamentCodeOffset(b []byte) int {
	if len(b) < tournamentNameLengthOffset+2 {
		return -1
	}
	nameLen := int(binary.LittleEndian.Uint16(b[tournamentNameLengthOffset:]))
	// MK8D encodes the name as a UTF-16 string terminated by U+0000.
	if nameLen < 2 || nameLen&1 != 0 {
		return -1
	}
	pos := tournamentNameLengthOffset + 2 + nameLen
	if pos >= len(b) {
		return -1
	}

	// Walk property list until 0xFF terminator
	for pos < len(b) {
		idByte := b[pos]
		if idByte == 0xff {
			// Terminator entry (0xFF 0x00 0x00 0x00 0x00)
			codeOffset := pos + 5
			if codeOffset+3 <= len(b) {
				return codeOffset
			}
			return -1
		}
		if pos+2 > len(b) {
			return -1
		}
		typeByte := b[pos+1]
		switch typeByte {
		case 0x01:
			pos += 4
		case 0x02:
			pos += 5
		case 0x04:
			pos += 7
		default:
			// Type de propriete inconnu. On NE retombe PAS sur l'ancien scan
			// "01 00 00" : c'est exactement l'heuristique ambigue que ce correctif
			// remplace (un entier ultérieur peut contenir ce motif, ce qui decalait
			// le code et faisait planter le client Pia). Mieux vaut echouer et le
			// signaler que deviner et corrompre la structure.
			log.Printf("[tournament] type de propriete inconnu 0x%02x a l'offset %d — offset du code non resolu", typeByte, pos)
			return -1
		}
	}
	return -1
}

func validTournamentCode(code string) bool {
	if len(code) != tournamentCodeLen {
		return false
	}
	for i := range code {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

func tournamentCodeMatches(b []byte, code string) bool {
	i := tournamentCodeOffset(b)
	if i < 0 || !validTournamentCode(code) {
		return false
	}
	if i+2 > len(b) {
		return false
	}
	n := int(binary.LittleEndian.Uint16(b[i:]))
	if n != tournamentCodeLen+1 || i+2+n > len(b) || b[i+2+n-1] != 0 {
		return false
	}
	return bytes.Equal(b[i+2:i+2+tournamentCodeLen], []byte(code))
}

func validStoredTournament(t *Tournament) bool {
	if t == nil || t.ID == 0 || len(t.Blob) < 12 || len(t.Blob) > tournamentMaxBlob+tournamentCodeLen {
		return false
	}
	if binary.LittleEndian.Uint32(t.Blob) != t.ID {
		return false
	}
	return tournamentCodeMatches(t.Blob, t.Code)
}

// stampTournament pose l'identifiant, la marque du tournoi et le code dans la
// structure envoyée par le créateur, et rend la structure complétée.
func stampTournament(payload []byte, id uint32, mark []byte, code string) []byte {
	out := make([]byte, len(payload))
	copy(out, payload)
	if len(out) < 12 {
		return out
	}
	out[0], out[1], out[2], out[3] = byte(id), byte(id>>8), byte(id>>16), byte(id>>24)
	copy(out[4:12], mark)
	i := tournamentCodeOffset(out)
	if i < 0 || !validTournamentCode(code) || !bytes.Equal(out[i:i+3], []byte{1, 0, 0}) {
		return out
	}
	// La chaîne vide (3 octets) devient une chaîne de 12 chiffres + terminateur.
	str := make([]byte, 0, 2+len(code)+1)
	n := len(code) + 1
	str = append(str, byte(n), byte(n>>8))
	str = append(str, code...)
	str = append(str, 0)

	res := make([]byte, 0, len(out)+len(str)-3)
	res = append(res, out[:i]...)
	res = append(res, str...)
	res = append(res, out[i+3:]...)
	return res
}

// CreateTournament enregistre le tournoi d'un joueur et rend sa structure
// complétée. Exporté pour qu'un serveur de jeu puisse l'alimenter autrement.
func (m *Matchmaking) CreateTournament(owner uint64, payload []byte) *Tournament {
	tournamentInit()
	i := tournamentCodeOffset(payload)
	if len(payload) == 0 || len(payload) > tournamentMaxBlob || i < 0 || !bytes.Equal(payload[i:i+3], []byte{1, 0, 0}) {
		return nil
	}
	tournaments.mu.Lock()
	if len(tournaments.byID) >= tournamentMax {
		tournaments.mu.Unlock()
		return nil
	}
	id := tournaments.nextID
	tournaments.nextID++
	mark := make([]byte, 8)
	_, _ = rand.Read(mark) // marque propre au tournoi, comme Nintendo
	t := &Tournament{
		ID: id, Owner: owner, Code: newTournamentCode(), Mark: mark,
		Participants: []uint64{owner}, CreatedAt: time.Now().Unix(),
	}
	t.Blob = stampTournament(payload, id, mark, t.Code)
	if !validStoredTournament(t) {
		tournaments.mu.Unlock()
		return nil
	}
	tournaments.byID[id] = t
	tournaments.mu.Unlock()
	tournamentDirty.Store(true)
	fmt.Printf("[Tournoi] créé id=%d par pid=%d code=%s (%d octets)\n", id, owner, t.Code, len(t.Blob))
	return t
}

func tournamentByID(id uint32) *Tournament {
	tournamentInit()
	tournaments.mu.RLock()
	defer tournaments.mu.RUnlock()
	t := tournaments.byID[id]
	if !validStoredTournament(t) {
		return nil
	}
	return t
}

func allTournaments() []*Tournament {
	tournamentInit()
	tournaments.mu.RLock()
	defer tournaments.mu.RUnlock()
	out := make([]*Tournament, 0, len(tournaments.byID))
	for _, t := range tournaments.byID {
		if validStoredTournament(t) {
			out = append(out, t)
		}
	}
	return out
}

// joinTournament inscrit un joueur. Sans effet s'il y est déjà.
func joinTournament(id uint32, pid uint64) bool {
	tournamentInit()
	tournaments.mu.Lock()
	defer tournaments.mu.Unlock()
	t := tournaments.byID[id]
	if t == nil || !validStoredTournament(t) {
		return false
	}
	for _, p := range t.Participants {
		if p == pid {
			return true
		}
	}
	t.Participants = append(t.Participants, pid)
	tournamentDirty.Store(true)
	return true
}

// --- handlers ---------------------------------------------------------------

// createTournament : structure entrante (u8 version, u32 longueur, contenu) →
// même forme en sortie, identifiants posés.
func (m *Matchmaking) createTournament(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	if len(req.Body) < 5 {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}
	ver := req.Body[0]
	ln := int(uint32(req.Body[1]) | uint32(req.Body[2])<<8 | uint32(req.Body[3])<<16 | uint32(req.Body[4])<<24)
	if ln <= 0 || 5+ln > len(req.Body) {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}
	t := m.CreateTournament(conn.PID, req.Body[5:5+ln])
	if t == nil {
		return NewRMCError(s, ProtocolMatchmakeExtension, req.CallID, ResultCoreInvalidArgument)
	}
	out := NewStreamOut(s)
	out.U8(ver)
	out.U32(uint32(len(t.Blob)))
	out.Write(t.Blob)
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

// writeTournamentList sérialise une liste de tournois : u32 nombre, puis par
// entrée u8 version, u32 longueur, contenu. Forme relevée sur la measured.
func writeTournamentList(s *Settings, list []*Tournament) []byte {
	out := NewStreamOut(s)
	valid := make([]*Tournament, 0, len(list))
	for _, t := range list {
		if validStoredTournament(t) {
			valid = append(valid, t)
		}
	}
	out.U32(uint32(len(valid)))
	for _, t := range valid {
		out.U8(1)
		out.U32(uint32(len(t.Blob)))
		out.Write(t.Blob)
	}
	return out.Bytes()
}

// tournamentsByIDs répond à « donne-moi ces tournois » (liste d'identifiants).
func (m *Matchmaking) tournamentsByIDs(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	in := NewStreamIn(req.Body, s)
	n := in.U32()
	var found []*Tournament
	for i := uint32(0); i < n && in.Err() == nil; i++ {
		if t := tournamentByID(in.U32()); t != nil {
			found = append(found, t)
		}
	}
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, writeTournamentList(s, found))
}

// searchTournaments répond à la recherche. On rend tout ce qu'on a : le jeu
// filtre lui-même sur ses critères, et notre volume est sans commune mesure
// avec les 85 de Nintendo.
func (m *Matchmaking) searchTournaments(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	list := allTournaments()
	if len(list) > 100 {
		list = list[:100]
	}
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, writeTournamentList(s, list))
}

// deleteTournament supprime un tournoi. RÉSERVÉ À SON PROPRIÉTAIRE : sans ce
// contrôle, n'importe qui pourrait effacer le tournoi d'un autre en devinant un
// identifiant, qui est un simple entier croissant.
func deleteTournament(id uint32, pid uint64) bool {
	tournamentInit()
	tournaments.mu.Lock()
	defer tournaments.mu.Unlock()
	t := tournaments.byID[id]
	if t == nil || !validStoredTournament(t) || t.Owner != pid {
		return false
	}
	delete(tournaments.byID, id)
	tournamentDirty.Store(true)
	return true
}

// removeTournament : suppression demandée par le jeu (identifiant sur 4 octets).
// On acquitte même si rien n'a été supprimé — un tournoi déjà absent n'est pas
// une erreur du point de vue du joueur, et refuser bloquait son écran.
func (m *Matchmaking) removeTournament(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	if len(req.Body) >= 4 {
		id := uint32(req.Body[0]) | uint32(req.Body[1])<<8 | uint32(req.Body[2])<<16 | uint32(req.Body[3])<<24
		if deleteTournament(id, conn.PID) {
			fmt.Printf("[Tournoi] supprimé id=%d par son propriétaire pid=%d\n", id, conn.PID)
		} else {
			fmt.Printf("[Tournoi] suppression refusée id=%d pid=%d (inconnu ou pas le propriétaire)\n", id, conn.PID)
		}
	}
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// registerTournament inscrit l'appelant. Nintendo répond un corps vide.
func (m *Matchmaking) registerTournament(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings
	if len(req.Body) >= 4 {
		id := uint32(req.Body[0]) | uint32(req.Body[1])<<8 | uint32(req.Body[2])<<16 | uint32(req.Body[3])<<24
		if joinTournament(id, conn.PID) {
			fmt.Printf("[Tournoi] pid=%d inscrit au tournoi %d\n", conn.PID, id)
		}
	}
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, nil)
}

// TournamentSummaries rend la liste courte servie par Ranking : par tournoi,
// 7 entiers de 32 bits — identifiant, nombre d'inscrits, puis des compteurs que
// Nintendo laisse souvent à zéro.
func TournamentSummaries() []byte {
	list := allTournaments()
	var body []byte
	put32 := func(v uint32) {
		body = append(body, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	put32(uint32(len(list)))
	for _, t := range list {
		body = append(body, 1) // version
		put32(28)              // longueur de l'entrée
		put32(t.ID)
		put32(uint32(len(t.Participants)))
		put32(4) // constant sur les 85 exemplaires de la measured
		put32(0)
		put32(0)
		put32(0)
		put32(0)
	}
	return body
}
