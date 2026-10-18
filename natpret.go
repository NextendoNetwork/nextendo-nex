package nex

import (
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// natpret.go : une station ne vaut que pour la session Pia qui l'a produite.
//
// Chaque fois qu'une console entre dans un salon, en cree un ou en sort, sa Pia ouvre un
// nouveau socket et le declare au serveur par un ReplaceURL : nouveau port, nouveau CID.
// Mesure sur les journaux de prod SSBU du 15/10 21:58 au 16/10 07:21 : entre deux
// ReplaceURL d'un meme joueur separes par une participation (0x6d.38/39/40, 0x32.1), le
// CID change 2151 fois sur 2151 et le port 2150 fois sur 2151. Tant que ce ReplaceURL
// n'est pas arrive, les stations gardees pour la connexion decrivent donc une session
// morte (ou, juste apres le Register, le port=1 sans CID ni natf/natm).
//
// Deux chemins servaient pourtant ces stations sans attendre :
//   - GetSessionURLs : le pont NAT (natbridge.go) sert l'URL perimee de l'hote a celui qui
//     le rejoint. Sa garde « RVCID == 0 » ne joue jamais ici, le Register pose deja le
//     RVCID. Cas du 16/10 01:37:56.386 : port 62723 servi au lieu de 49549, sans CID.
//   - InitiateProbe : la demande de sondage arrive a l'hote avant sa nouvelle session, il
//     n'y repond jamais, et celui qui le rejoint reste 25 s sans verdict (01:37:57.103 et
//     22:46:37.415 le 15/10).
//
// Une connexion est donc « prete » quand elle a fait au moins un ReplaceURL depuis sa
// derniere remise a zero (Register, autoMatchmake, creation ou entree dans un salon,
// EndParticipation). Une connexion qui n'a jamais ete remise a zero est prete : c'est le
// comportement d'avant, et rien ne change pour un hote deja pret.
//
// Attendre n'a de sens que si un ReplaceURL est encore ATTENDU (natAttendu). Rejeu sur les
// journaux de prod du 15/10 21:58 au 16/10 07:20 : attendre tout hote pas pret servait 8
// visiteurs apres le ReplaceURL de l'hote, mais en retardait 222 de 2,5 s pour rien, et
// retenait 70 InitiateProbe 2,5 s sans aucun verdict. Un hote pas pret n'est donc attendu
// que s'il vient d'entrer dans un salon (natAttenteParticipation), ou d'en sortir alors que
// le salon est encore ouvert (natAttenteRetour), et s'il n'est ni une console qui sonde deja
// sans ReplaceURL (sonde), ni une console dont une participation entiere est passee sans
// ReplaceURL (muette). Apres un Register seul, aucun ReplaceURL ne vient : 0 sur 1619 dans
// les 10 s. Un hote pas pret mais pas attendu est servi tout de suite, exactement comme avant.
//
// Effet mesure (rejeu independant de cette regle, prod du 15/10 21:58:13 au 16/10 09:33:51,
// 11,59 h, etat tenu par connexion) : sur 2360 GetSessionURLs, 7 sont differes de +0,5 a
// +1,8 s, aucun n'attend les 2,5 s ; sur 5364 InitiateProbe, 7 sont liberes par le
// ReplaceURL de la cible (+0,08 a +0,97 s) et 2 coupes (+0,5 s et +1,0 s). Le gain est
// MOINDRE que les « 16 cas gagnants » de la premiere mesure : l'un d'eux est une connexion
// perimee deja prete (rien ne change), 7 paires avaient reussi en prod (le correctif ne
// leur ajoute qu'un retard), et le gain n'est plausible que pour 2 visiteurs. Il n'est pas
// prouve sur console. Hors echantillon (16/10 07:20 a 09:33) : 0 changement.

// natAttenteParticipation : delai dans lequel un ReplaceURL est attendu apres une
// participation (autoMatchmake, createSession, joinSession). Mesure (prod SSBU, 15/10 21:58
// au 16/10 08:39, 2718 ReplaceURL qui suivent une participation) : mediane 1,5 a 2,4 s,
// 99e centile 3,8 a 5,4 s, 8 seulement au-dela de 6 s. Gagnant le plus tardif : visiteur a
// 3,65 s de la participation de l'hote, ReplaceURL 0,46 s apres. Passe ce delai, le
// ReplaceURL n'est plus attendu : le visiteur recoit la reponse d'avant sans attendre.
var natAttenteParticipation = nouvelleDuree(6 * time.Second)

// natAttenteRetour : apres un EndParticipation, la console ne refait de ReplaceURL qu'apres
// une nouvelle participation. Mesure : sur 2006 EndParticipation suivis d'une participation
// avant tout ReplaceURL, 86 reviennent en 0,62 a 1 s, 2 entre 1 et 3 s, les autres plus
// tard. Cas du 15/10 22:46:36 : l'hote sort a .42, revient dans le meme salon a 37.12 (le
// budget d'une participation reprend alors), ReplaceURL a 37.89.
var natAttenteRetour = nouvelleDuree(time.Second)

// probeRetenuMax borne la retenue d'un InitiateProbe vers une cible pas prete. Meme valeur
// que l'attente de GetSessionURLs (hostReplaceURLWait) : passe ce delai, il part quand meme,
// exactement comme avant ce correctif.
var probeRetenuMax = nouvelleDuree(2500 * time.Millisecond)

// hoteMuetDelai : une cible qui n'a pas fait pousser d'InitiateProbe en retour (0x3.3 vers
// l'appelant) dans ce delai apres un probeinit=1 est journalisee « hote muet ».
var hoteMuetDelai = nouvelleDuree(3 * time.Second)

// Plafonds. Tout ce que ce fichier garde en memoire (InitiateProbe retenus, tentatives,
// minuteries, attentes de GetSessionURLs) est borne PAR CONNEXION : sans eux, un seul
// RequestProbeInitiationExt qui nomme des milliers de fois une cible pas prete retenait
// des milliers d'InitiateProbe et de minuteries (mesure de la revue : 100 000 cibles en
// 3,8 Mo de requete, +52,6 Mio de tas retenus, pic 279 Mio, au-dela de la memoire du
// conteneur). Passe un plafond, on revient au chemin d'avant ce correctif (envoi ou
// reponse immediats), qui ne retient rien.
//
// Valeurs mesurees sur les journaux de prod SSBU du 15/10 21:59 au 16/10 08:35 (5340
// RequestProbeInitiationExt, 2346 GetSessionURLs) : chaque plafond est au moins 3 fois le
// maximum observe.
const (
	// natCiblesMax : entrees de la liste cible traitees par RequestProbeInitiationExt. Les
	// suivantes sont ignorees. Prod : 1 entree (5307 fois), 2 (15), 3 (1), jamais plus ; 7
	// listes nomment deux ou trois fois la meme cible, elles restent servies telles quelles.
	natCiblesMax = 32

	// natStationMax : longueur maximale (octets) de la station poussee par
	// RequestProbeInitiationExt. Au-dela, aucun InitiateProbe : la station etait recopiee
	// vers chaque cible, gardee 2,5 s par cible pas prete et journalisee en entier pour
	// chacune, jusqu'a la borne MaxRMCSize (1 Mio). Prod SSBU du 16/10 03:40 a 12:57 UTC :
	// 1908 stations journalisees, 172 octets au plus (toutes les URL prudp des journaux
	// aussi), soit pres de 6 fois moins.
	natStationMax = 1024

	// natRetenusMax : InitiateProbe retenus a la fois pour une meme cible. Au-dela, envoi
	// immediat comme avant. Prod : 5 InitiateProbe au plus vers une meme connexion en 2,5 s.
	natRetenusMax = 16

	// natTentativesMax : cibles dont une connexion suit le sondage (journaux « hote muet »
	// et « abandon silencieux »). Prod : 3 au plus entre deux remises a zero.
	natTentativesMax = 32

	// natMuetsMax : minuteries « hote muet » armees a la fois pour une connexion. Prod : 5
	// probeinit=1 au plus en 3 s pour un meme appelant.
	natMuetsMax = 16

	// natAttentesURLMax : GetSessionURLs d'une meme connexion en attente du ReplaceURL d'un
	// hote. Au-dela, reponse immediate comme avant. Prod : 6 au plus en 2,5 s pour un PID.
	natAttentesURLMax = 16
)

// dureeReglable : une duree que les tests raccourcissent pendant que des minuteries d'autres
// tests la lisent encore. Atomique pour que -race reste muet ; la prod ne la change jamais.
type dureeReglable struct{ ns atomic.Int64 }

func nouvelleDuree(d time.Duration) *dureeReglable {
	r := &dureeReglable{}
	r.ns.Store(int64(d))

	return r
}

func (r *dureeReglable) get() time.Duration  { return time.Duration(r.ns.Load()) }
func (r *dureeReglable) set(d time.Duration) { r.ns.Store(int64(d)) }

// natDiagSortie detourne les journaux de ce fichier (tests). Vide : sortie standard.
var natDiagSortie atomic.Pointer[func(string, ...any)]

// natDiagf ecrit un journal [NAT/diag] de l'etat « pret ».
func natDiagf(format string, args ...any) {
	if f := natDiagSortie.Load(); f != nil {
		(*f)(format, args...)

		return
	}
	fmt.Printf(format, args...)
}

// natEtat est l'etat « pret » d'une connexion et ce qui en depend. Protege par mu : il est
// ecrit par la goroutine de la connexion (Register, ReplaceURL, matchmaking) et lu par
// celles des autres (GetSessionURLs, RequestProbeInitiationExt).
type natEtat struct {
	// pasPret : remise a zero, aucun ReplaceURL depuis. Valeur nulle = pret, pour qu'une
	// connexion jamais remise a zero garde le comportement d'avant.
	pasPret  bool
	razQuoi  string
	razQuand time.Time
	razGenre natGenre

	// sonde : la connexion a demande un sondage (RequestProbeInitiationExt) depuis sa
	// remise a zero, sans ReplaceURL : sa Pia tourne sans declarer de nouveau socket.
	// Mesure : 37 InitiateProbe sur 37 retenus vers une telle cible, aucun ReplaceURL dans
	// les 2,5 s ni aucun verdict.
	sonde bool

	// muette : une participation precedente a passe natAttenteParticipation sans ReplaceURL
	// (console qui n'en envoie pas, cas releve en prod le 15/10). Effacee par le ReplaceURL.
	muette bool

	// dernierCID : CID du dernier ReplaceURL. Le meme CID renvoye est un doublon (mesure :
	// 66 doublons sans participation entre), pas une nouvelle session.
	dernierCID int

	// retenus : InitiateProbe destines a CETTE connexion, en attente de son ReplaceURL. Ne
	// contient que des entrees pas encore parties (retrait a l'envoi), natRetenusMax au plus.
	retenus []*probeRetenu

	// retenusPleinSignale : le plafond natRetenusMax a ete journalise pour l'episode en
	// cours (une ligne par episode, pas une par entree refusee).
	retenusPleinSignale bool

	// tentatives : sondages probeinit=1 que CETTE connexion a fait pousser, par ID de
	// connexion de la cible, en attente de leur verdict (0x3.4 / 0x3.7). natTentativesMax
	// cibles au plus.
	tentatives map[uint32]*tentativeNAT

	// muetsArmes : minuteries « hote muet » en cours pour les sondages de CETTE connexion.
	muetsArmes int

	// attentesURL : GetSessionURLs de CETTE connexion qui attendent le ReplaceURL d'un hote.
	attentesURL int
}

// natGenre classe la derniere remise a zero, pour natAttendu.
type natGenre uint8

const (
	natGenreRegister      natGenre = iota + 1 // Register : pas de socket Pia avant une participation
	natGenreParticipation                     // autoMatchmake, createSession, joinSession
	natGenreSortie                            // EndParticipation
)

func natGenreDe(quoi string) natGenre {
	switch quoi {
	case "Register":
		return natGenreRegister
	case "EndParticipation":
		return natGenreSortie
	}

	return natGenreParticipation
}

// probeRetenu est un InitiateProbe mis en file. parti est garde par le mu de la cible :
// le ReplaceURL et la fin du delai se disputent l'envoi, un seul l'emporte.
type probeRetenu struct {
	appelant  *Connection
	msg       *RMCMessage
	rvcid     uint32
	station   string
	probeinit bool
	depuis    time.Time
	max       time.Duration
	parti     bool
}

// tentativeNAT est un sondage probeinit=1 livre a une cible, sans verdict pour l'instant.
type tentativeNAT struct {
	ciblePID uint64
	depuis   time.Time
	delai    time.Duration
	repondu  bool
	// callID : appel de l'InitiateProbe (0xFFFF0000 + celui du RequestProbeInitiationExt).
	// Une liste qui nomme deux fois la meme cible (7 fois en prod du 15/10 21:59 au 16/10
	// 08:35) envoie deux InitiateProbe du meme appel : c'est la meme tentative.
	callID uint32
}

// natRAZ : la connexion commence une nouvelle session. Ses stations decrivent l'ancienne
// jusqu'a son prochain ReplaceURL. Ses sondages encore sans verdict sont abandonnes.
func (c *Connection) natRAZ(quoi string) {
	maintenant := time.Now()
	c.natMu.Lock()
	// Une participation entiere passee sans ReplaceURL : la console n'en envoie pas, on ne
	// l'attendra plus jusqu'a son prochain ReplaceURL.
	if c.nat.pasPret && c.nat.razGenre == natGenreParticipation && maintenant.Sub(c.nat.razQuand) >= natAttenteParticipation.get() {
		c.nat.muette = true
	}
	c.nat.pasPret = true
	c.nat.razQuoi = quoi
	c.nat.razQuand = maintenant
	c.nat.razGenre = natGenreDe(quoi)
	c.nat.sonde = false
	abandons := c.natPrendreTentativesLocked()
	c.natMu.Unlock()

	c.natJournaliserAbandons(abandons, quoi)
}

// natEstPret dit si la connexion a fait un ReplaceURL depuis sa derniere remise a zero.
func (c *Connection) natEstPret() bool {
	c.natMu.Lock()
	defer c.natMu.Unlock()

	return !c.nat.pasPret
}

// natRaz rend la cause et l'age de la derniere remise a zero, pour les journaux.
func (c *Connection) natRaz() (string, time.Duration) {
	c.natMu.Lock()
	defer c.natMu.Unlock()
	if c.nat.razQuand.IsZero() {
		return "aucune", 0
	}

	return c.nat.razQuoi, time.Since(c.nat.razQuand)
}

// natDerniereRAZ rend le genre de la derniere remise a zero (0 : aucune).
func (c *Connection) natDerniereRAZ() natGenre {
	c.natMu.Lock()
	defer c.natMu.Unlock()

	return c.nat.razGenre
}

// natAttendu dit si un ReplaceURL de cette connexion pas prete est encore attendu, donc si
// un visiteur ou un InitiateProbe gagne a l'attendre. salonOuvert : le salon que le
// visiteur demande accepte encore autoMatchmake (seul chemin de retour mesure apres un
// EndParticipation) ; toujours vrai pour un InitiateProbe. Faux pour une connexion prete :
// l'appelant suit alors le chemin d'avant. raison sert aux journaux.
func (c *Connection) natAttendu(salonOuvert bool) (bool, string) {
	c.natMu.Lock()
	defer c.natMu.Unlock()

	return c.natAttenduLocked(time.Now(), salonOuvert)
}

func (c *Connection) natAttenduLocked(maintenant time.Time, salonOuvert bool) (bool, string) {
	e := &c.nat
	if !e.pasPret {
		return false, "devenue prete"
	}
	if e.sonde {
		return false, "elle sonde deja ses pairs sans ReplaceURL"
	}
	if e.muette {
		return false, "une participation precedente est passee sans ReplaceURL"
	}
	age := maintenant.Sub(e.razQuand)
	switch e.razGenre {
	case natGenreParticipation:
		if budget := natAttenteParticipation.get(); age >= budget {
			return false, fmt.Sprintf("%s il y a %dms, plus de %v", e.razQuoi, age.Milliseconds(), budget)
		}

		return true, ""
	case natGenreSortie:
		if !salonOuvert {
			return false, "sortie d'un salon ferme, pas de retour possible par autoMatchmake"
		}
		if budget := natAttenteRetour.get(); age >= budget {
			return false, fmt.Sprintf("sortie il y a %dms sans retour en %v", age.Milliseconds(), budget)
		}

		return true, ""
	}

	return false, "Register seul, pas de ReplaceURL avant une participation"
}

// natPrendreAttenteURL reserve, pour cette connexion (le visiteur), une attente de
// GetSessionURLs. false au-dela de natAttentesURLMax : l'appelant repond alors tout de
// suite, comme avant ce correctif. Chaque true est rendu par natRendreAttenteURL.
func (c *Connection) natPrendreAttenteURL() bool {
	c.natMu.Lock()
	defer c.natMu.Unlock()
	if c.nat.attentesURL >= natAttentesURLMax {
		return false
	}
	c.nat.attentesURL++

	return true
}

func (c *Connection) natRendreAttenteURL() {
	c.natMu.Lock()
	c.nat.attentesURL--
	c.natMu.Unlock()
}

// natSonde : la connexion demande un sondage (RequestProbeInitiationExt).
func (c *Connection) natSonde() {
	c.natMu.Lock()
	c.nat.sonde = true
	c.natMu.Unlock()
}

// natApresReplaceURL marque la connexion prete et prend la file de ses InitiateProbe
// retenus, que l'appelant envoie APRES la reponse au ReplaceURL (natEnvoyerRetenus). Un
// nouveau CID est une nouvelle session : les sondages de l'ancienne sans verdict sont
// abandonnes.
func (c *Connection) natApresReplaceURL(cid int) []*probeRetenu {
	c.natMu.Lock()
	c.nat.pasPret = false
	c.nat.muette = false
	var abandons []tentativeAbandon
	if cid != 0 && cid != c.nat.dernierCID {
		if c.nat.dernierCID != 0 {
			abandons = c.natPrendreTentativesLocked()
		}
		c.nat.dernierCID = cid
	}
	var aEnvoyer []*probeRetenu
	for _, p := range c.nat.retenus {
		if !p.parti {
			p.parti = true
			aEnvoyer = append(aEnvoyer, p)
		}
	}
	c.nat.retenus = nil
	c.nat.retenusPleinSignale = false
	c.natMu.Unlock()

	c.natJournaliserAbandons(abandons, "nouveau ReplaceURL")

	return aEnvoyer
}

// natEnvoyerRetenus envoie les InitiateProbe retenus pour cette connexion, maintenant prete.
func (c *Connection) natEnvoyerRetenus(retenus []*probeRetenu) {
	for _, p := range retenus {
		natDiagf("[NAT/diag] InitiateProbe de pid=%d vers pid=%d LIBERE par son ReplaceURL (retenu %dms)\n",
			p.appelant.PID, c.PID, time.Since(p.depuis).Milliseconds())
		c.natEnvoyerProbe(p.appelant, p.msg, p.rvcid, p.station, p.probeinit)
	}
}

// natRetenir met en file l'InitiateProbe destine a cette connexion si elle n'est pas prete
// et que son ReplaceURL est attendu (natAttendu), et rend true. Sinon elle rend false sans
// rien mettre en file : l'appelant envoie tout de suite, comme avant.
func (c *Connection) natRetenir(appelant *Connection, msg *RMCMessage, rvcid uint32, station string, probeinit bool) bool {
	maintenant := time.Now()
	c.natMu.Lock()
	if !c.nat.pasPret {
		c.natMu.Unlock()

		return false
	}
	quoi, age := c.nat.razQuoi, maintenant.Sub(c.nat.razQuand)
	if attendu, raison := c.natAttenduLocked(maintenant, true); !attendu {
		c.natMu.Unlock()
		natDiagf("[NAT/diag] InitiateProbe de pid=%d vers pid=%d envoye sans retenue : cible pas prete (%s il y a %dms) mais aucun ReplaceURL attendu (%s)\n",
			appelant.PID, c.PID, quoi, age.Milliseconds(), raison)

		return false
	}
	// File pleine : envoi immediat, le chemin d'avant (aucune structure retenue). Une seule
	// ligne de journal par episode de file pleine.
	if len(c.nat.retenus) >= natRetenusMax {
		signaler := !c.nat.retenusPleinSignale
		c.nat.retenusPleinSignale = true
		c.natMu.Unlock()
		if signaler {
			natDiagf("[NAT/diag] InitiateProbe vers pid=%d : %d deja retenus (plafond), les suivants partent sans retenue comme avant\n",
				c.PID, natRetenusMax)
		}

		return false
	}
	p := &probeRetenu{appelant: appelant, msg: msg, rvcid: rvcid, station: station, probeinit: probeinit, depuis: maintenant, max: probeRetenuMax.get()}
	c.nat.retenus = append(c.nat.retenus, p)
	c.natMu.Unlock()

	natDiagf("[NAT/diag] InitiateProbe de pid=%d vers pid=%d RETENU : cible pas prete (%s il y a %dms, aucun ReplaceURL depuis), %v au plus\n",
		appelant.PID, c.PID, quoi, age.Milliseconds(), p.max)
	c.natArmerRetenu(p)

	return true
}

// natArmerRetenu reprogramme la verification d'un InitiateProbe retenu au tour suivant
// (hostReplaceURLPoll, comme l'attente de GetSessionURLs), sans depasser son delai.
func (c *Connection) natArmerRetenu(p *probeRetenu) {
	d := hostReplaceURLPoll
	if reste := p.max - time.Since(p.depuis); reste < d {
		d = max(reste, 0)
	}
	time.AfterFunc(d, func() {
		defer natProtege("natReverifierRetenu")
		c.natReverifierRetenu(p)
	})
}

// natProtege recupere une panique dans une goroutine ou une minuterie de ce coeur. Hors de
// la boucle de lecture gws (seule protegee par gws.Recovery), une panique tuerait tout le
// processus, donc le jeu pour tous les joueurs. La ligne passe par fmt et non par natDiagf,
// au cas ou la panique viendrait de la sortie du journal elle-meme.
func natProtege(ou string) {
	if r := recover(); r != nil {
		fmt.Printf("[NAT/diag] PANIQUE recuperee dans %s : %v\n%s", ou, r, debug.Stack())
	}
}

// natReverifierRetenu envoie quand meme un InitiateProbe que le ReplaceURL n'a pas libere,
// des que son delai est passe ou que le ReplaceURL de la cible n'est plus attendu (sortie
// sans retour, budget de la participation epuise). La cible recoit alors ce qu'elle aurait
// recu avant ce correctif, en retard. Un retour dans un salon pendant la retenue relance le
// budget : la retenue continue (cas du 15/10 23:59:16).
func (c *Connection) natReverifierRetenu(p *probeRetenu) {
	maintenant := time.Now()
	c.natMu.Lock()
	if p.parti {
		c.natMu.Unlock()

		return
	}
	ecoule := maintenant.Sub(p.depuis)
	attendu, raison := c.natAttenduLocked(maintenant, true)
	if ecoule < p.max && attendu {
		c.natMu.Unlock()
		c.natArmerRetenu(p)

		return
	}
	p.parti = true
	// Retrait en gardant l'ordre d'arrivee (le ReplaceURL libere dans cet ordre) : la file
	// compte natRetenusMax entrees au plus, le parcours est donc borne.
	for i, q := range c.nat.retenus {
		if q == p {
			c.nat.retenus = append(c.nat.retenus[:i], c.nat.retenus[i+1:]...)

			break
		}
	}
	if len(c.nat.retenus) == 0 {
		c.nat.retenus = nil
		c.nat.retenusPleinSignale = false
	}
	c.natMu.Unlock()
	// Envoi en defer : il part meme si le journal ci-dessous panique (la panique est
	// ensuite recuperee par natProtege). Avant, une panique du journal laissait l'InitiateProbe
	// jamais envoye et la minuterie « hote muet » jamais armee (TestPaniquesDesMinuteriesNATRecuperees
	// echouait lance seul).
	defer c.natEnvoyerProbe(p.appelant, p.msg, p.rvcid, p.station, p.probeinit)

	if ecoule >= p.max {
		natDiagf("[NAT/diag] InitiateProbe de pid=%d vers pid=%d envoye QUAND MEME apres %v : toujours aucun ReplaceURL\n",
			p.appelant.PID, c.PID, p.max)
	} else {
		natDiagf("[NAT/diag] InitiateProbe de pid=%d vers pid=%d envoye QUAND MEME apres %dms : ReplaceURL plus attendu (%s)\n",
			p.appelant.PID, c.PID, ecoule.Milliseconds(), raison)
	}
}

// natEnvoyerProbe pousse l'InitiateProbe a cette connexion (la cible) et, pour un
// probeinit=1, ouvre la tentative de l'appelant. La ligne « InitiateProbe VERS » garde son
// format d'avant et part a l'heure de l'envoi reel : les scripts de mesure s'y fient.
func (c *Connection) natEnvoyerProbe(appelant *Connection, msg *RMCMessage, rvcid uint32, station string, probeinit bool) {
	fmt.Printf("[NAT/diag] pushInitiateProbe caller=%d -> InitiateProbe VERS pid=%d (id=%d rvcid=%d) station=%q\n",
		appelant.PID, c.PID, c.ID, rvcid, station)
	c.SendRMC(msg)
	if probeinit {
		appelant.natDebutTentative(c, msg.CallID)
	}
}

// natDebutTentative ouvre la tentative de c vers cible et arme le constat « hote muet ».
// Purement diagnostique : au-dela de natTentativesMax cibles suivies, une nouvelle cible
// n'est pas suivie ; au-dela de natMuetsMax minuteries en cours, la tentative est suivie
// (« abandon silencieux ») sans minuterie « hote muet ». L'envoi n'en depend pas. Le
// meme appel vers la meme cible (cible nommee plusieurs fois dans une liste) garde la
// tentative en cours : ni « relance », ni minuterie de plus.
func (c *Connection) natDebutTentative(cible *Connection, callID uint32) {
	t := &tentativeNAT{ciblePID: cible.PID, depuis: time.Now(), delai: hoteMuetDelai.get(), callID: callID}

	c.natMu.Lock()
	if c.nat.tentatives == nil {
		c.nat.tentatives = map[uint32]*tentativeNAT{}
	}
	ancienne := c.nat.tentatives[cible.ID]
	if ancienne != nil && ancienne.callID == callID {
		c.natMu.Unlock()

		return
	}
	if ancienne == nil && len(c.nat.tentatives) >= natTentativesMax {
		c.natMu.Unlock()

		return
	}
	c.nat.tentatives[cible.ID] = t
	armer := c.nat.muetsArmes < natMuetsMax
	if armer {
		c.nat.muetsArmes++
	}
	c.natMu.Unlock()

	if ancienne != nil {
		c.natJournaliserAbandons([]tentativeAbandon{{ancienne}}, "relance vers la meme cible")
	}
	if armer {
		time.AfterFunc(t.delai, func() {
			defer natProtege("natVerifierMuet")
			c.natVerifierMuet(cible, t)
		})
	}
}

// natVerifierMuet journalise une cible qui n'a pas repondu au sondage dans le delai.
func (c *Connection) natVerifierMuet(cible *Connection, t *tentativeNAT) {
	c.natMu.Lock()
	c.nat.muetsArmes--
	muet := c.nat.tentatives[cible.ID] == t && !t.repondu
	c.natMu.Unlock()
	if !muet {
		return
	}
	quoi, age := cible.natRaz()
	natDiagf("[NAT/diag] hote muet : pid=%d n'a pas repondu en %v a l'InitiateProbe (probeinit=1) de pid=%d (cible prete=%v, derniere remise a zero : %s il y a %dms)\n",
		cible.PID, t.delai, c.PID, cible.natEstPret(), quoi, age.Milliseconds())
}

// natReponseDe : la connexion d'ID cibleID fait pousser un InitiateProbe vers c, elle
// repond donc au sondage que c lui avait envoye.
func (c *Connection) natReponseDe(cibleID uint32) {
	c.natMu.Lock()
	if t := c.nat.tentatives[cibleID]; t != nil {
		t.repondu = true
	}
	c.natMu.Unlock()
}

// natVerdict : c a rapporte le verdict de son percage vers la connexion d'ID cibleID.
func (c *Connection) natVerdict(cibleID uint32) {
	c.natMu.Lock()
	delete(c.nat.tentatives, cibleID)
	c.natMu.Unlock()
}

type tentativeAbandon struct{ t *tentativeNAT }

// natPrendreTentativesLocked vide les tentatives sans verdict (natMu tenu).
func (c *Connection) natPrendreTentativesLocked() []tentativeAbandon {
	if len(c.nat.tentatives) == 0 {
		return nil
	}
	out := make([]tentativeAbandon, 0, len(c.nat.tentatives))
	for _, t := range c.nat.tentatives {
		out = append(out, tentativeAbandon{t})
	}
	c.nat.tentatives = nil

	return out
}

// natJournaliserAbandons : un joueur a quitte une tentative sans en rapporter le verdict.
// Ces joins perdus n'apparaissent nulle part ailleurs (aucun 0x3.4) ; mesure du diagnostic :
// 59 episodes en 6 h, absents des 1167 verdicts.
func (c *Connection) natJournaliserAbandons(abandons []tentativeAbandon, cause string) {
	for _, a := range abandons {
		natDiagf("[NAT/diag] abandon silencieux : pid=%d quitte sa tentative vers pid=%d sans verdict (+%.1fs, cause : %s, la cible a repondu=%v)\n",
			c.PID, a.t.ciblePID, time.Since(a.t.depuis).Seconds(), cause, a.t.repondu)
	}
}
