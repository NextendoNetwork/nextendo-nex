package nex

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AutoMatchmakeParamComplet est la requete 0x6D.0x28 (AutoMatchmakeWithParam_Postpone) lue en
// ENTIER. AutoMatchmakeParam ne lit que la session ; le reste etait ignore.
//
// Ordre RELEVE dans le serialiseur du jeu (Smash 13.0.5, main reloge 0x252520, appele par la
// methode 0x6D.0x28 en 0x24e30c) : en-tete de Structure, MatchmakeSession (+0x10, 0x254b00),
// List<PID> (std::list +0x160, nombre +0x170), u32 +0x178, u32 +0x17c, String +0x180 (0x228510),
// u16 +0x1a8, List<MatchmakeSessionSearchCriteria> (+0x1b0, nombre +0x1c0, 0x255f00 par
// element), List<u32> (+0x1c8, nombre +0x1d8), MatchmakeBlockListParam (+0x1e0, 0x252cf0).
// Vecteurs : testdata/appariement/auto_*.hex, produits par 0x252520 en emulation
// (un outil interne).
type AutoMatchmakeParamComplet struct {
	Session                  MatchmakeSession
	Participants             []uint64
	GIDForParticipationCheck uint32
	Option                   uint32
	JoinMessage              string
	ParticipationCount       uint16
	Criteres                 []MatchmakeSessionSearchCriteria
	GIDsCibles               []uint32
	BlockList                MatchmakeBlockListParam
}

// Levels implements Structure.
func (a *AutoMatchmakeParamComplet) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			o.Add(&a.Session)
			WriteList(o, a.Participants, func(o *StreamOut, v uint64) { o.PID(v) })
			o.U32(a.GIDForParticipationCheck)
			o.U32(a.Option)
			o.String(a.JoinMessage)
			o.U16(a.ParticipationCount)
			WriteList(o, a.Criteres, func(o *StreamOut, c MatchmakeSessionSearchCriteria) { o.Add(&c) })
			WriteList(o, a.GIDsCibles, func(o *StreamOut, v uint32) { o.U32(v) })
			o.Add(&a.BlockList)
		},
		Load: func(i *StreamIn) {
			i.Extract(&a.Session)
			a.Participants = ReadList(i, func(i *StreamIn) uint64 { return i.PID() })
			a.GIDForParticipationCheck = i.U32()
			a.Option = i.U32()
			a.JoinMessage = i.String()
			a.ParticipationCount = i.U16()
			a.Criteres = ReadList(i, func(i *StreamIn) MatchmakeSessionSearchCriteria {
				var c MatchmakeSessionSearchCriteria
				i.Extract(&c)
				return c
			})
			a.GIDsCibles = ReadList(i, func(i *StreamIn) uint32 { return i.U32() })
			i.Extract(&a.BlockList)
		},
	}}
}

// lireAutoMatchmakeComplet lit toute la requete 0x6D.0x28. complet est faux quand la fin
// (apres la session) est illisible, y compris un contenu tronque A L'INTERIEUR d'une Structure
// (lecture stricte, newStreamInStrict) : l'appelant garde alors le comportement d'avant, fonde
// sur la seule session (lue par AutoMatchmakeParam).
func lireAutoMatchmakeComplet(body []byte, s *Settings) (p AutoMatchmakeParamComplet, complet bool) {
	in := newStreamInStrict(body, s)
	in.Extract(&p)
	return p, in.Err() == nil
}

// lireCritereStrict lit le critere de 0x6D.0x34 en lecture stricte (filtre des arenes).
func lireCritereStrict(body []byte, s *Settings) (c MatchmakeSessionSearchCriteria, ok bool) {
	in := newStreamInStrict(body, s)
	in.Extract(&c)
	return c, in.Err() == nil
}

// BORNES DES CRITERES. Tout critere est evalue sous m.mu, pour chaque salon candidat : sans
// borne, un client qui envoie 1 Mio de criteres (12 000 criteres, ou 330 000 attributs) tenait
// le verrou de 150 a 200 ms par requete pour 150 salons, et gelait l'appariement de tous. Le jeu
// envoie UN critere de 6 attributs par recherche (0x168ab2c-0x168ab5c pour 0x6D.0x28, 0x168b240
// pour 0x6D.0x34), chaque champ ecrit par « %u » ou « %u,%u » (0x1aff70 : 21 octets au plus,
// « 4294967295,4294967295 »). Hors de ces bornes (avec une marge), les criteres ne sont pas
// appliques : comportement d'avant.
const (
	criteresMax       = 4  // criteres par requete (le jeu : 1)
	critereAttribsMax = 6  // attributs par critere (le jeu : 6)
	critereChampMax   = 32 // octets par champ numerique (le jeu : 21 au plus)
	critereCodeMax    = 64 // octets du mot de code
)

// criteresBornes : les criteres tiennent dans les bornes (voir plus haut), et il y en a au moins un.
func criteresBornes(cs []MatchmakeSessionSearchCriteria) bool {
	if len(cs) == 0 || len(cs) > criteresMax {
		return false
	}
	for i := range cs {
		c := &cs[i]
		if len(c.Attribs) > critereAttribsMax || len(c.Codeword) > critereCodeMax {
			return false
		}
		for _, a := range c.Attribs {
			if len(a) > critereChampMax {
				return false
			}
		}
		for _, f := range []string{c.GameMode, c.MinParticipants, c.MaxParticipants, c.MatchmakeSystem} {
			if len(f) > critereChampMax {
				return false
			}
		}
	}
	return true
}

// critereValeur dit si v satisfait un champ de critere : "" = tout, "v" = egal, "a,b" = de a a b
// inclus. Ce sont les deux formes que le jeu ecrit (0x1aff70 « %u » : 0x2559a0 valeur exacte,
// 0x255af0 plage, appeles par 0x113d70 en 0x114918 / 0x1148e0). Un champ illisible ne filtre
// pas (connu=false) : le serveur ne doit pas fermer une recherche sur un format qu'il ne
// comprend pas.
func critereValeur(c string, v uint32) (ok, connu bool) {
	c = strings.TrimSpace(c)
	if c == "" {
		return true, true
	}
	if a, b, plage := strings.Cut(c, ","); plage {
		lo, e1 := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
		hi, e2 := strconv.ParseUint(strings.TrimSpace(b), 10, 32)
		if e1 != nil || e2 != nil {
			return true, false
		}
		return uint64(v) >= lo && uint64(v) <= hi, true
	}
	x, err := strconv.ParseUint(c, 10, 32)
	if err != nil {
		return true, false
	}
	return uint64(v) == x, true
}

// critereAccepte dit si la session cand (nb participants presents) satisfait le critere c.
func critereAccepte(c *MatchmakeSessionSearchCriteria, cand *MatchmakeSession, nb int, ignorer int) bool {
	return critereRefus(c, cand, nb, ignorer) == ""
}

// critereRefus rend le premier champ du critere c que la session cand (nb participants
// presents) ne satisfait pas, ou "" si elle le satisfait : "mode", "attribut0".."attribut5",
// "min", "max", "systeme", "places", "verrou", "mdp", "code". Les noms forment un ensemble fini
// (journal des refus de 0x6D.0x34, browseNoHolder).
//
// FORMAT PROUVE (0x113d70 -> 0x255f00, vecteurs critere_*.hex) ; la REGLE de comparaison est
// celle de NEX (hypothese : le serveur de Nintendo n'est pas observable), appliquee champ par
// champ : GameMode, Attribs[i] (sauf l'indice ignorer), Min/MaxParticipants, MatchmakeSystem,
// VacantOnly, ExcludeLocked, VacantParticipants, ExcludeUserPassword, ExcludeSysPassword,
// Codeword. Les parametres (@RV, @DR, @VR, @SI, @UsGI, @CC, _RU) servent a la methode de
// selection 5 du serveur (SelectionMethod, 0xeb680 : 2 cote Pia -> 5) et ne sont pas un filtre.
//
// GAMEMODE : EGALITE EXACTE, genres 7 et 8 compris. Le genre 8 est l'arene « amis seulement » :
//   - creation d'arene (0x189d7e4-0x189d800) : genre = 7, plus 1 si l'octet 0x53069b1 vaut 2 ;
//   - 0x53069b1 est le reglage « qui peut entrer » de l'arene a creer : le menu n'a que deux
//     choix, 0 -> ligne 0 et 2 -> ligne 1 (0x1c7fdc0-0x1c7fdf4) ; il est recopie en +0xa8 de la
//     fiche de l'arene (0x16eda10-0x16eda24), et la liste des arenes lit ce meme +0xa8
//     (0x1bd8298) pour choisir l'animation : 0 « room_status_any », 1 « room_status_acq »,
//     2 « room_status_friend » ou « room_status_friend_lock » (0x1bd9d48-0x1bd9e1c, chaines
//     0x43cf32b, 0x44128e0, 0x4318d94, 0x42a2eea).
//
// Le jeu range donc lui-meme les arenes d'amis dans un autre genre que les arenes ouvertes a
// tous ; la recherche publique demande le genre 7 (vecteur critere_arene_filtres). Une arene de
// genre 8 ne sort pas d'une recherche de genre 7, et reste visible des amis de ses membres par
// la liste des arenes d'amis (0x6D.0x33, areneAmisVisible, qui ne regarde pas le genre). Elle
// ne porterait de toute facon pas l'attribut 0 que la recherche exige : le jeu ne l'ecrit que
// pour les genres 1, 2, 3, 5, 7 et 13 (masque 0x20ae, 0x16969d4-0x16969fc).
func critereRefus(c *MatchmakeSessionSearchCriteria, cand *MatchmakeSession, nb int, ignorer int) string {
	if ok, _ := critereValeur(c.GameMode, cand.GameMode); !ok {
		return "mode"
	}
	for i, a := range c.Attribs {
		if i == ignorer {
			continue
		}
		var v uint32
		if i < len(cand.Attribs) {
			v = cand.Attribs[i]
		}
		if ok, _ := critereValeur(a, v); !ok {
			if i < len(nomsAttributs) {
				return nomsAttributs[i]
			}
			return "attribut"
		}
	}
	if ok, _ := critereValeur(c.MinParticipants, uint32(cand.MinParticipants)); !ok {
		return "min"
	}
	if ok, _ := critereValeur(c.MaxParticipants, uint32(cand.MaxParticipants)); !ok {
		return "max"
	}
	if ok, _ := critereValeur(c.MatchmakeSystem, cand.MatchmakeSystem); !ok {
		return "systeme"
	}
	libres := int(cand.MaxParticipants) - nb
	if c.VacantOnly && libres <= 0 {
		return "places"
	}
	if c.VacantParticipants > 0 && libres < int(c.VacantParticipants) {
		return "places"
	}
	if c.ExcludeLocked && !cand.OpenParticipation {
		return "verrou"
	}
	if c.ExcludeUserPassword && sessionProtegee(cand) {
		return "mdp"
	}
	if c.ExcludeSysPassword && cand.SystemPasswordEnabled {
		return "mdp"
	}
	if c.Codeword != "" && cand.Codeword != c.Codeword {
		return "code"
	}
	return ""
}

// nomsAttributs : noms des refus par attribut (critereRefus), un par attribut d'une session
// Smash (6, 0x115930).
var nomsAttributs = [...]string{"attribut0", "attribut1", "attribut2", "attribut3", "attribut4", "attribut5"}

// unCritereAccepte : la session satisfait au moins un critere de la liste (le jeu en envoie un
// seul par recherche pour la Partie rapide : la requete 0x1689ac0 n'en recopie qu'un, compte 1
// en 0x168ab5c). Liste vide : rien ne filtre.
func unCritereAccepte(cs []MatchmakeSessionSearchCriteria, cand *MatchmakeSession, nb, ignorer int) bool {
	if len(cs) == 0 {
		return true
	}
	for i := range cs {
		if critereAccepte(&cs[i], cand, nb, ignorer) {
			return true
		}
	}
	return false
}

// PaliersAppariement regle l'appariement fidele de 0x6D.0x28 pour une connexion.
//
// CE QUE FAIT LE JEU (Smash 13.0.5). La session creee porte en attribut 0 la reponse de 0x6D.0x3A,
// en attribut 1 la tranche de niveau (0x1696914-0x169695c : premier i tel que le reglage Utility
// 1101+i depasse la note, 22 tranches, reglage 1100), en attribut 2 le drapeau Smash Elite
// (genre 3, 0x1696960-0x1696984), et MaxParticipants = 2 pour le 1 contre 1, 4 pour Smash a 4
// (0x168ae80-0x168aef8). Le critere envoye demande l'attribut 0 et l'attribut 2 exacts et
// l'attribut 1 dans [tranche-l, tranche+l] (0x168aa20-0x168aa88). Deux formes alternent selon
// l'etat +0x26a50 de la scene (0x1689e04) : nul, l vient des reglages Utility pairs
// 0x49c/0x49e/0x4a0 (Partie rapide, 1), 0x4a2 (genre 2, 7) ou 0x4a4/0x4a6/0x4a8 (Elite, 1) et
// l'etat passe a 120 (reglage 1199, 0x168a5ac-0x168a5b8) ; non nul, reglages impairs (2, 9, 2),
// etat remis a 0 (0x168a938) et le joueur Elite accepte aussi une session non Elite (attribut
// 2 dans [0,1], 0x168aa78). L'etat est aussi ecrit ailleurs (0x16899fc, 0x169c3b8,
// 0x169ef34) : « une recherche sur deux » est une hypothese sur l'enchainement exact. Le format (MaxParticipants) n'est PAS dans le critere : il n'y figure que
// par le parametre « _RU » (regles preferees), interprete par le serveur.
//
// CE QUE FAIT LE SERVEUR. Un salon candidat recoit un palier :
//
//	0 : un critere du jeu l'accepte et il a le meme MaxParticipants que la session demandee ;
//	1 : idem en ignorant l'attribut AttributNiveau (la tranche de niveau), meme format ;
//	2 : idem au palier 1, mais d'un autre format ;
//	3 : tout salon du meme GameMode (le comportement d'avant, Elite compris).
//
// Le palier 0 est toujours permis ; les paliers 1, 2 et 3 le sont quand l'attente atteint
// Niveau, Format et Tout. L'attente est la plus longue des deux : depuis la creation du salon
// candidat (un hote seul s'ouvre peu a peu a plus de joueurs) et depuis la premiere recherche
// du joueur dans sa serie en cours (un joueur qui revient chercher apres une attente vaine
// elargit tout de suite). Parmi les salons : d'abord les salons permis, puis le palier le plus
// bas, puis l'ecart de tranche le plus petit, puis le salon qui attend depuis le plus longtemps.
//
// UN SALON REFUSE EST PRIS FAUTE DE MIEUX, il ne fait jamais ouvrir un salon de plus. Refuser
// tout a fait (l'arrivant ouvre son propre salon) fragmentait la petite population : les deux
// hotes attendent ensuite seuls que l'un relance, ce qu'un hote seul ne fait qu'au bout de 52 s
// en mediane (p90 138 s, prod du 17/10). Mesure de la relecture (prod du 17/10 06:37Z-12:29Z,
// 217 jonctions de Partie rapide rejouees avec la tranche, le format et le drapeau Elite de
// chaque arrivant) : 51 (23 %) auraient ete refusees par 10/25/45 s. Le salon retenu est donc toujours pris dans les memes
// categories qu'avant (hote en ligne, puis paire NAT stricte, puis hote absent), et un arrivant
// entre exactement quand il entrait avant ; les paliers ne decident que lequel. RefusDes > 0
// retablit le refus quand au moins RefusDes salons candidats ont ete evalues (une population
// assez grande pour qu'un salon refuse ne manque a personne) ; 0 (defaut) : jamais.
type PaliersAppariement struct {
	AttributNiveau int
	Niveau         time.Duration
	Format         time.Duration
	Tout           time.Duration
	RefusDes       int
}

// refusStrict dit si, apres l'evaluation de candidats salons, un salon refuse doit etre ecarte
// (l'arrivant ouvre alors son propre salon) plutot que pris faute de mieux.
func (p *PaliersAppariement) refusStrict(candidats int) bool {
	return p.RefusDes > 0 && candidats >= p.RefusDes
}

// permis dit si un palier est permis apres une attente donnee.
func (p *PaliersAppariement) permis(palier int, attente time.Duration) bool {
	switch palier {
	case 0:
		return true
	case 1:
		return attente >= p.Niveau
	case 2:
		return attente >= p.Format
	default:
		return attente >= p.Tout
	}
}

// palier rend le palier d'un salon candidat (voir PaliersAppariement).
func (p *PaliersAppariement) palier(cs []MatchmakeSessionSearchCriteria, src, cand *MatchmakeSession, nb int) int {
	memeFormat := cand.MaxParticipants == src.MaxParticipants
	if memeFormat && unCritereAccepte(cs, cand, nb, -1) {
		return 0
	}
	sansNiveau := unCritereAccepte(cs, cand, nb, p.AttributNiveau)
	switch {
	case sansNiveau && memeFormat:
		return 1
	case sansNiveau:
		return 2
	default:
		return 3
	}
}

// ecartNiveau : distance entre l'attribut de niveau de la session demandee et celui du salon.
func ecartNiveau(src, cand *MatchmakeSession, i int) uint32 {
	var a, b uint32
	if i >= 0 && i < len(src.Attribs) {
		a = src.Attribs[i]
	}
	if i >= 0 && i < len(cand.Attribs) {
		b = cand.Attribs[i]
	}
	if a > b {
		return a - b
	}
	return b - a
}

// serieRechercheMax : au-dela, une serie de recherches est oubliee (le joueur a quitte la
// Partie rapide ou joue depuis longtemps).
const serieRechercheMax = 10 * time.Minute

// debutSerie (sous m.mu) rend le debut de la serie de recherches de pid, et la commence si
// besoin.
func (m *Matchmaking) debutSerie(pid uint64, now time.Time) time.Time {
	if m.series == nil {
		m.series = map[uint64]time.Time{}
	}
	d, ok := m.series[pid]
	if !ok || now.Sub(d) > serieRechercheMax {
		d = now
		m.series[pid] = d
	}
	if len(m.series) > 4096 {
		for p, t := range m.series {
			if now.Sub(t) > serieRechercheMax {
				delete(m.series, p)
			}
		}
	}
	return d
}

// finSeries (sous m.mu) : ces joueurs sont dans un salon a plusieurs, leur serie est finie.
func (m *Matchmaking) finSeries(pids []uint64) {
	for _, p := range pids {
		delete(m.series, p)
	}
}

// JOURNAUX BORNES. Les champs viennent du client (une requete fait jusqu'a 1 Mio) : chaque
// liste et chaque chaine est tronquee AVANT d'etre mise en forme, une ligne de journal reste
// donc de quelques Ko au plus quoi que le client envoie.
const (
	journalElemsMax  = 8  // elements montres d'une liste (attributs, criteres, cles)
	journalChaineMax = 32 // octets montres d'une chaine
)

// texteChaine : chaine entre guillemets, tronquee a journalChaineMax octets.
func texteChaine(v string) string {
	if len(v) <= journalChaineMax {
		return strconv.Quote(v)
	}
	return fmt.Sprintf("%s...(%d octets)", strconv.Quote(v[:journalChaineMax]), len(v))
}

// texteChaines : liste de chaines, au plus journalElemsMax elements tronques.
func texteChaines(vs []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vs {
		if i == journalElemsMax {
			fmt.Fprintf(&b, " ...(%d en tout)", len(vs))
			break
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(texteChaine(v))
	}
	b.WriteByte(']')
	return b.String()
}

// texteAttribs : attributs d'une session, au plus journalElemsMax.
func texteAttribs(a []uint32) string {
	if len(a) <= journalElemsMax {
		return fmt.Sprint(a)
	}
	return fmt.Sprintf("%v...(%d en tout)", a[:journalElemsMax], len(a))
}

// texteCriteres resume les criteres pour le journal (au plus criteresMax criteres montres).
func texteCriteres(cs []MatchmakeSessionSearchCriteria) string {
	var b strings.Builder
	for i := range cs {
		if i == criteresMax {
			fmt.Fprintf(&b, " | ...(%d criteres en tout)", len(cs))
			break
		}
		c := &cs[i]
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "mode=%s attribs=%s min=%s max=%s sys=%s vacant=%v/%d locked=%v sel=%d mdp=%v code=%s",
			texteChaine(c.GameMode), texteChaines(c.Attribs), texteChaine(c.MinParticipants), texteChaine(c.MaxParticipants),
			texteChaine(c.MatchmakeSystem), c.VacantOnly, c.VacantParticipants, c.ExcludeLocked, c.SelectionMethod,
			c.ExcludeUserPassword, texteChaine(c.Codeword))
		if len(c.Param.Params) > 0 {
			fmt.Fprintf(&b, " param=%s", texteParam(c.Param.Params))
		}
	}
	return b.String()
}

// texteCriteresCourts : genre et attributs de chaque critere (au plus criteresMax), pour le
// journal de tous les joueurs qui ont l'appariement ou le filtre (borne comme texteCriteres).
func texteCriteresCourts(cs []MatchmakeSessionSearchCriteria) string {
	var b strings.Builder
	for i := range cs {
		if i == criteresMax {
			fmt.Fprintf(&b, " | ...(%d criteres en tout)", len(cs))
			break
		}
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "mode=%s attribs=%s", texteChaine(cs[i].GameMode), texteChaines(cs[i].Attribs))
	}
	if len(cs) == 0 {
		b.WriteString("aucun")
	}
	return b.String()
}

// texteRaisons : salons ecartes par champ, noms tries (critereRefus en rend au plus 15).
func texteRaisons(r map[string]int) string {
	cles := make([]string, 0, len(r))
	for k := range r {
		cles = append(cles, k)
	}
	sort.Strings(cles)
	var b strings.Builder
	b.WriteByte('(')
	for i, k := range cles {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%d", k, r[k])
	}
	b.WriteByte(')')
	return b.String()
}

// texteParam : parametres d'une session ou d'un critere, cles triees ; au-dela de
// 2*journalElemsMax cles, seulement leur nombre (le tri lui-meme couterait).
func texteParam(p map[string]Variant) string {
	if len(p) > 2*journalElemsMax {
		return fmt.Sprintf("{%d cles}", len(p))
	}
	cles := make([]string, 0, len(p))
	for k := range p {
		cles = append(cles, k)
	}
	sort.Strings(cles)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range cles {
		if i > 0 {
			b.WriteByte(' ')
		}
		v := p[k]
		nom := k
		if len(nom) > journalChaineMax {
			nom = texteChaine(k)
		}
		switch v.Type {
		case VariantInt64:
			fmt.Fprintf(&b, "%s=%d", nom, v.Int)
		case VariantUint64:
			fmt.Fprintf(&b, "%s=%#x", nom, v.Uint)
		case VariantBool:
			fmt.Fprintf(&b, "%s=%v", nom, v.Bool)
		case VariantString:
			fmt.Fprintf(&b, "%s=%s", nom, texteChaine(v.String))
		default:
			fmt.Fprintf(&b, "%s=?%d", nom, v.Type)
		}
	}
	b.WriteByte('}')
	return b.String()
}

// attributOuZero : attribut i, ou 0 s'il manque.
func attributOuZero(a []uint32, i int) uint32 {
	if i >= 0 && i < len(a) {
		return a[i]
	}
	return 0
}
