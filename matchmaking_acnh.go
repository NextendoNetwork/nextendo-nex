package nex

// Recherche de session par PARTICIPANT et par CODE (« Dodo Code »).
//
// Ces deux mécanismes sont le cœur de l'online d'Animal Crossing: New Horizons :
//   - rendre visite à un ami        -> MatchmakeExtension 0x6D.0x33 FindMatchmakeSessionByParticipant
//   - rejoindre via un Dodo Code    -> MatchmakeExtension 0x6D.0x34 Browse..., code porté par
//                                      le champ CodeWord des critères de recherche (NEX 4.0.0)
//
// Le cœur répondait « liste vide » aux deux : suffisant pour les arènes Smash (où 0x33 n'est
// appelé qu'au démarrage), mais cela revient à dire à ACNH « ton ami n'a aucune île ouverte »
// à chaque tentative de visite. D'où une implémentation réelle, adossée au store en mémoire.
//
// ⚠ Neutre par défaut. La recherche par participant est derrière un interrupteur
// (FindByParticipantEnabled) qu'ACNH seul active, et la recherche par code ne s'applique que
// si les critères en portent un : Mario Kart, Splatoon 2 et Smash gardent le comportement
// qu'ils ont aujourd'hui.
//
// Formats fil repris de la documentation NintendoClients (MIT) présente dans le dépôt —
// la documentation publique NintendoClients (MIT), nintendo/nex/matchmaking.py — et d'elle seule.

import (
	"fmt"
)

// MatchmakeSessionSearchCriteria porte les critères d'une recherche de session.
// Ordre des champs : NintendoClients matchmaking.py MatchmakeSessionSearchCriteria.load.
// Les blocs 3.5.0 / 4.0.0 sont conditionnés par la version NEX, comme à la source.
type MatchmakeSessionSearchCriteria struct {
	Attribs             []string
	GameMode            string
	MinParticipants     string
	MaxParticipants     string
	MatchmakeSystem     string
	VacantOnly          bool
	ExcludeLocked       bool
	ExcludeNonHostPID   bool
	SelectionMethod     uint32
	VacantParticipants  uint16
	Param               MatchmakeParam
	ExcludeUserPassword bool
	ExcludeSysPassword  bool
	ReferGID            uint32
	Codeword            string
	Range               ResultRange
}

// Levels implémente Structure.
func (c *MatchmakeSessionSearchCriteria) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			WriteList(o, c.Attribs, func(o *StreamOut, v string) { o.String(v) })
			o.String(c.GameMode)
			o.String(c.MinParticipants)
			o.String(c.MaxParticipants)
			o.String(c.MatchmakeSystem)
			o.Bool(c.VacantOnly)
			o.Bool(c.ExcludeLocked)
			o.Bool(c.ExcludeNonHostPID)
			o.U32(c.SelectionMethod)
			if o.Settings.NexVersion >= 30500 {
				o.U16(c.VacantParticipants)
			}
			if o.Settings.NexVersion >= 40000 {
				o.Add(&c.Param)
				o.Bool(c.ExcludeUserPassword)
				o.Bool(c.ExcludeSysPassword)
				o.U32(c.ReferGID)
				o.String(c.Codeword)
				o.Add(&c.Range)
			}
		},
		Load: func(i *StreamIn) {
			c.Attribs = ReadList(i, func(i *StreamIn) string { return i.String() })
			c.GameMode = i.String()
			c.MinParticipants = i.String()
			c.MaxParticipants = i.String()
			c.MatchmakeSystem = i.String()
			c.VacantOnly = i.Bool()
			c.ExcludeLocked = i.Bool()
			c.ExcludeNonHostPID = i.Bool()
			c.SelectionMethod = i.U32()
			if i.Settings.NexVersion >= 30500 {
				c.VacantParticipants = i.U16()
			}
			if i.Settings.NexVersion >= 40000 {
				i.Extract(&c.Param)
				c.ExcludeUserPassword = i.Bool()
				c.ExcludeSysPassword = i.Bool()
				c.ReferGID = i.U32()
				c.Codeword = i.String()
				i.Extract(&c.Range)
			}
		},
	}}
}

// FindMatchmakeSessionByParticipantParam : liste de PID à localiser + options + block-list.
type FindMatchmakeSessionByParticipantParam struct {
	PrincipalIDs []uint64
	Options      uint32
	BlockList    MatchmakeBlockListParam
}

// Levels implémente Structure.
func (p *FindMatchmakeSessionByParticipantParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			WriteList(o, p.PrincipalIDs, func(o *StreamOut, v uint64) { o.PID(v) })
			o.U32(p.Options)
			o.Add(&p.BlockList)
		},
		Load: func(i *StreamIn) {
			p.PrincipalIDs = ReadList(i, func(i *StreamIn) uint64 { return i.PID() })
			p.Options = i.U32()
			i.Extract(&p.BlockList)
		},
	}}
}

// FindMatchmakeSessionByParticipantResult apparie un participant à SA session.
//
// La réponse est une liste de ces paires {PID, session} et NON une liste de sessions nues :
// renvoyer des sessions nues décale la lecture côté client (il lit la session là où il attend
// le PID) et ACNH tombe en 2306-0116.
type FindMatchmakeSessionByParticipantResult struct {
	PrincipalID uint64
	Session     MatchmakeSession
}

// Levels implémente Structure.
func (r *FindMatchmakeSessionByParticipantResult) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.PID(r.PrincipalID)
			o.Add(&r.Session)
		},
		Load: func(i *StreamIn) {
			r.PrincipalID = i.PID()
			i.Extract(&r.Session)
		},
	}}
}

// sessionOfParticipant renvoie la session la PLUS RÉCENTE à laquelle pid participe, ou nil.
// Verrou déjà tenu par l'appelant.
func (m *Matchmaking) sessionOfParticipant(pid uint64) *gathering {
	var best *gathering
	var bestAt uint64
	for _, g := range m.gatherings {
		if g.session == nil {
			continue
		}
		for _, p := range g.participants {
			if p != pid {
				continue
			}
			// StartedTime est un horodatage NEX encodé : plus grand = plus récent.
			if at := g.session.StartedTime.Value(); best == nil || at > bestAt {
				best, bestAt = g, at
			}
			break
		}
	}
	return best
}

// findByParticipant répond à FindMatchmakeSessionByParticipant (0x6D.0x33).
//
// Pour chaque PID demandé, on renvoie la session qu'il occupe. Un ami sans île ouverte ne
// produit simplement aucune entrée : une liste vide est la réponse CORRECTE (« personne à
// visiter »), pas une erreur — renvoyer une erreur afficherait un code de communication.
//
// La session est renvoyée ENTIÈRE (ApplicationBuffer et MatchmakeParam compris). ACNH lit
// dans MatchmakeParam ses propres clés de session pour valider l'île ; une session amputée
// est rejetée par le visiteur, qui se retrouve devant « aucune île ».
func (m *Matchmaking) findByParticipant(conn *Connection, req *RMCMessage) *RMCMessage {
	s := conn.Settings

	var param FindMatchmakeSessionByParticipantParam
	in := NewStreamIn(req.Body, s)
	in.Extract(&param)

	var results []*FindMatchmakeSessionByParticipantResult
	m.mu.Lock()

	// Liste vide = « n'importe quelle session ouverte ». Voir le commentaire du drapeau.
	if len(param.PrincipalIDs) == 0 && m.FindByParticipantVideVeutDireToutes {
		for _, g := range m.gatherings {
			if g.session == nil || len(g.participants) == 0 {
				continue
			}
			// On n'annonce pas au joueur la session ou il est DEJA : il chercherait a
			// rejoindre la sienne, ce qui est le meme cul-de-sac qu'avant, en plus
			// difficile a lire dans le journal.
			deja := false
			for _, p := range g.participants {
				if p == conn.PID {
					deja = true
					break
				}
			}
			if deja {
				continue
			}
			r := *g.session
			r.UserPassword = ""
			// Le PID annonce est celui de l'HOTE : c'est lui que le demandeur va
			// rejoindre, et le client s'en sert pour identifier la partie.
			results = append(results, &FindMatchmakeSessionByParticipantResult{
				PrincipalID: g.participants[0], Session: r,
			})
		}
		m.mu.Unlock()
		out := NewStreamOut(s)
		out.U32(uint32(len(results)))
		for _, r := range results {
			out.Add(r)
		}
		fmt.Printf("[MM] findByParticipant(pids=[] = toutes) pid=%d -> %d session(s) ouverte(s)\n",
			conn.PID, len(results))
		return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
	}

	for _, pid := range param.PrincipalIDs {
		g := m.sessionOfParticipant(pid)
		if g == nil {
			continue
		}
		r := *g.session
		// La clé de session DOIT sortir dès la découverte. Le serveur the previous stack de référence la
		// renvoie dans FindByParticipant (prouvé : mesure sur le serveur de reference, Find/51 = 481o
		// dont la clé de 32o ; scrubbée on tombe à 449o) et la Pia d'ACNH s'en sert pour amorcer
		// la session P2P AVANT le JOIN — la mettre à nil laisse le visiteur avec une clé nulle,
		// le handshake P2P n'aboutit pas et la visite cale sur « Getting ready to depart »
		// (2618-0502). Ici le demandeur est un ami autorisé à visiter : aucune fuite. Le mot de
		// passe reste retiré (ACNH n'en pose pas : le laissez-passer d'île est le Dodo Code).
		r.UserPassword = ""
		results = append(results, &FindMatchmakeSessionByParticipantResult{PrincipalID: pid, Session: r})
	}
	m.mu.Unlock()

	out := NewStreamOut(s)
	out.U32(uint32(len(results)))
	for _, r := range results {
		out.Add(r)
	}
	fmt.Printf("[MM] findByParticipant(pids=%v) -> %d session(s)\n", param.PrincipalIDs, len(results))
	return NewRMCSuccess(s, ProtocolMatchmakeExtension, req.Method, req.CallID, out.Bytes())
}

// Bits de ModificationFlags (mesurés sur le stack de référence : un Dodo Code arrive avec
// flags = 0x4002 = OpenParticipation | Codeword).
const (
	modFlagOpenParticipation uint32 = 0x2
	modFlagCodeword          uint32 = 0x4000
)

// UpdateMatchmakeSessionParam décrit une mise à jour PARTIELLE d'une session (0x6D.0x2C).
// Le champ ModificationFlags indique lesquels des champs suivants sont significatifs.
// Ordre : NintendoClients matchmaking.py UpdateMatchmakeSessionParam.load.
type UpdateMatchmakeSessionParam struct {
	GID                 uint32
	ModificationFlags   uint32
	Attributes          []uint32
	OpenParticipation   bool
	ApplicationBuffer   []byte
	ProgressScore       uint8
	Param               MatchmakeParam
	StartedTime         DateTime
	UserPassword        string
	GameMode            uint32
	Description         string
	MinParticipants     uint16
	MaxParticipants     uint16
	MatchmakeSystem     uint32
	ParticipationPolicy uint32
	PolicyArgument      uint32
	Codeword            string
}

// Levels implémente Structure.
func (p *UpdateMatchmakeSessionParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.U32(p.GID)
			o.U32(p.ModificationFlags)
			WriteList(o, p.Attributes, func(o *StreamOut, v uint32) { o.U32(v) })
			o.Bool(p.OpenParticipation)
			o.Buffer(p.ApplicationBuffer)
			o.U8(p.ProgressScore)
			o.Add(&p.Param)
			o.DateTime(p.StartedTime.Value())
			o.String(p.UserPassword)
			o.U32(p.GameMode)
			o.String(p.Description)
			o.U16(p.MinParticipants)
			o.U16(p.MaxParticipants)
			o.U32(p.MatchmakeSystem)
			o.U32(p.ParticipationPolicy)
			o.U32(p.PolicyArgument)
			o.String(p.Codeword)
		},
		Load: func(i *StreamIn) {
			p.GID = i.U32()
			p.ModificationFlags = i.U32()
			p.Attributes = ReadList(i, func(i *StreamIn) uint32 { return i.U32() })
			p.OpenParticipation = i.Bool()
			p.ApplicationBuffer = i.Buffer()
			p.ProgressScore = i.U8()
			i.Extract(&p.Param)
			p.StartedTime = DateTime(i.DateTime())
			p.UserPassword = i.String()
			p.GameMode = i.U32()
			p.Description = i.String()
			p.MinParticipants = i.U16()
			p.MaxParticipants = i.U16()
			p.MatchmakeSystem = i.U32()
			p.ParticipationPolicy = i.U32()
			p.PolicyArgument = i.U32()
			p.Codeword = i.String()
		},
	}}
}

// applySessionPart écrit réellement une mise à jour partielle dans la session.
//
// C'est ce qui fait vivre le « Dodo Code » : quand un hôte ouvre sa porte avec un code,
// Animal Crossing envoie ces 5 caractères ICI, et nulle part ailleurs. Un handler qui se
// contente d'acquitter — ce que faisait le cœur, hérité de la fin de partie coop de
// Splatoon 2 — n'enregistre jamais le code, et la recherche par code ne peut alors JAMAIS
// correspondre : le visiteur reçoit « aucune île » quoi qu'il tape.
//
// Les champs sont appliqués selon ModificationFlags, mais on tolère un drapeau absent quand
// la valeur est manifestement renseignée : mieux vaut enregistrer un code que le perdre.
func (m *Matchmaking) applySessionPart(p *UpdateMatchmakeSessionParam) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	g := m.gatherings[p.GID]
	if g == nil || g.session == nil {
		return false
	}
	// Le Codeword s'écrit dès que son bit est posé OU qu'il est non vide (défensif : ne jamais
	// perdre un Dodo Code — c'est ce qui porte toute la recherche par code).
	if p.ModificationFlags&modFlagCodeword != 0 || p.Codeword != "" {
		g.session.Codeword = p.Codeword
		g.code = p.Codeword
		if p.Codeword != "" {
			m.byCode[p.Codeword] = p.GID
		}
	}
	if len(p.Attributes) > 0 {
		g.session.Attribs = append([]uint32(nil), p.Attributes...)
	}
	if len(p.ApplicationBuffer) > 0 {
		g.session.ApplicationData = append([]byte(nil), p.ApplicationBuffer...)
	}
	if p.GameMode != 0 {
		g.session.GameMode = p.GameMode
	}
	if p.MaxParticipants != 0 {
		g.session.MaxParticipants = p.MaxParticipants
	}
	// OpenParticipation n'est touché QUE si son bit est posé : un 0x2C d'un autre but ne doit
	// pas refermer (ou rouvrir) la porte par un OpenParticipation=false implicite.
	if p.ModificationFlags&modFlagOpenParticipation != 0 {
		g.session.OpenParticipation = p.OpenParticipation
	}
	return true
}

// NOTE sur l'ouverture d'aéroport (readiness de l'hôte).
//
// La pile the previous stack d'origine devait ajouter un contournement ici : après CreateMatchmakeSession,
// l'hôte d'ACNH attend ~10 s une notification confirmant que le rassemblement est vivant, et
// the previous stack excluait délibérément l'appelant de cette notification. Sans elle,
// l'hôte concluait à une erreur de communication, appelait UnregisterGathering et se
// déconnectait — porte fermée, aucun visiteur possible.
//
// Ce cœur n'a PAS ce défaut : notifyParticipation annonce l'appelant à tous les participants,
// lui compris (« self included »), et l'hôte étant seul participant à la création, il reçoit
// bien son propre Participate(3001) avec Param2 = son PID — exactement l'événement qu'attend
// sa table de stations. Le contournement est donc inutile ici, et l'ajouter enverrait un
// doublon. Documenté plutôt que codé, pour que personne ne le « rajoute » plus tard.

// GidDuParticipant rend l'identifiant de la partie a laquelle un joueur participe, ou
// zero s'il n'est dans aucune.
//
// POURQUOI IL EST EXPOSE. En cooperatif, les joueurs d'une meme partie demandent le niveau
// CHACUN DE LEUR COTE. Le serveur doit leur rendre le meme, et le seul lien qui les unit
// est l'identifiant de leur partie. Sans lui, il faudrait choisir le niveau d'apres quelque
// chose de global — l'heure, un compteur — et deux consoles a cheval sur la frontiere
// tomberaient dans deux niveaux differents.
//
// Prend le verrou lui-meme, contrairement a sessionOfParticipant que ses appelants
// verrouillent deja.
func (m *Matchmaking) GidDuParticipant(pid uint64) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	for gid, g := range m.gatherings {
		if g.session == nil {
			continue
		}
		for _, p := range g.participants {
			if p == pid {
				return gid
			}
		}
	}
	return 0
}
