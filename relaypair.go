package nex

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// Relais P2P a UN PORT PAR PAIRE de joueurs.
//
// Pourquoi pas un port unique partage. Un relais qui ecoute sur un seul port doit, pour
// chaque paquet, deviner a qui le faire suivre. Le trafic PRUDP porte les identifiants de
// connexion et se laisse aiguiller, mais la sonde de perçage de Pia fait 16 octets et ne
// porte RIEN : ni identifiant, ni destinataire. L essai du 21/08 sur Splatoon 2 s est
// arrete exactement la — 914 sondes reçues, aucune reexpediee possible, donc le relais y a
// repondu lui-meme, ce que Pia refuse : elle attend une reponse DU PAIR.
//
// Un port par paire supprime la question. Tout ce qui arrive sur le port d une paire
// appartient a cette paire : le paquet part vers l autre extremite, quel que soit son
// contenu. Sondes, perçage, montee de session, donnees de jeu — tout traverse, sans qu une
// seule ligne de code ait a comprendre le protocole.
//
// Le port se calcule des DEUX PID, sans etat partage : le chemin des URL de session et
// celui de l initiation de sonde tombent donc sur le meme port sans se parler.
//
// Inerte tant que SetPairRelay n a pas ete appele : aucun titre existant ne change.

// pairRelayTTL : au-dela de ce silence, la paire est consideree finie et son port libere.
// Une partie envoie en continu ; deux minutes sans un octet signifie que plus personne
// n est la. Assez long pour survivre a un ecran de chargement, assez court pour ne pas
// garder des milliers de sockets ouverts apres une soiree chargee.
const pairRelayTTL = 2 * time.Minute

var (
	pairMu       sync.RWMutex
	pairHost     string
	pairPortBase int
	pairPortSpan int

	pairSocksMu sync.Mutex
	pairSocks   = map[int]*pairSocket{}
	// pairPorts retient le port attribue a chaque paire. Depuis que le hachage peut etre
	// deplace par une collision, le port n est plus une fonction pure des PID : les deux
	// points d appel doivent donc lire la MEME attribution, pas la recalculer.
	pairPorts   = map[[2]uint64]int{}
	pairReaping bool

	// Cumul depuis le demarrage, garde sous pairSocksMu. Les compteurs d une paire
	// disparaissent avec elle ; sans ce cumul, juger le relais demande de lire le journal
	// a la main sur une fenetre choisie au hasard — c est ainsi qu on a annonce 96% la ou
	// il y avait 78%, et 14 eligibles la ou il y en eut 134.
	pairTotal PairTotaux
)

// PairTotaux est le bilan cumule du relais depuis le demarrage du serveur.
type PairTotaux struct {
	Recus   uint64
	Relayes uint64
	Rejets  uint64
	Remaps  uint64
	// PairesOuvertes compte les ports alloues, PairesMuettes ceux fermes sans avoir relaye
	// UN SEUL paquet. PairesMuettes est LE chiffre qui denonce un relais casse : une paire
	// muette est une partie qui n a pas eu lieu, et rien d autre ne la signale.
	PairesOuvertes uint64
	PairesMuettes  uint64
}

// PairStat decrit une paire vivante.
type PairStat struct {
	Port       int
	PIDs       [2]uint64
	Attendus   [2]string
	Places     [2]string // endpoints appris ; "" si la place est encore libre
	Recus      uint64
	Relayes    uint64
	Rejets     uint64
	Remaps     uint64
	Rejetees   map[string]uint64
	InactifSec int
}

// RelayStats est l etat complet du relais, pour le tableau de bord.
type RelayStats struct {
	Actif    bool
	Host     string
	PortBase int
	PortSpan int
	Paires   int
	Total    PairTotaux
	Vivantes []PairStat
}

// PairRelayStats rend un instantane du relais. Prend pairSocksMu puis chaque ps.mu, dans le
// meme ordre que le faucheur, donc sans nouvel ordre de verrouillage.
func PairRelayStats() RelayStats {
	host, base, span, on := pairRelayConfig()

	pairSocksMu.Lock()
	defer pairSocksMu.Unlock()

	st := RelayStats{
		Actif:    on,
		Host:     host,
		PortBase: base,
		PortSpan: span,
		Paires:   len(pairSocks),
		Total:    pairTotal,
		Vivantes: make([]PairStat, 0, len(pairSocks)),
	}

	maintenant := time.Now()
	for port, ps := range pairSocks {
		ps.mu.Lock()
		p := PairStat{
			Port:       port,
			PIDs:       [2]uint64{ps.pidLo, ps.pidHi},
			Attendus:   ps.attendus,
			Recus:      ps.recus,
			Relayes:    ps.relayes,
			Rejets:     ps.rejets,
			Remaps:     ps.remaps,
			InactifSec: int(maintenant.Sub(ps.dernier).Seconds()),
			// Copie : ne jamais rendre la carte vivante, le boucle ecrit dedans.
			Rejetees: make(map[string]uint64, len(ps.rejetVu)),
		}
		for i, vu := range ps.vus {
			if vu != nil {
				p.Places[i] = vu.String()
			}
		}
		for k, v := range ps.rejetVu {
			p.Rejetees[k] = v
		}
		ps.mu.Unlock()

		st.Vivantes = append(st.Vivantes, p)
	}

	return st
}

// SetPairRelay arme le relais par paire sur une plage de ports, ou le desarme avec un host
// vide. Les ports sont ouverts A LA DEMANDE, un par paire active : la plage borne
// l attribution, elle n est pas ecoutee d avance.
func SetPairRelay(host string, portBase, portSpan int) {
	pairMu.Lock()
	pairHost, pairPortBase, pairPortSpan = host, portBase, portSpan
	pairMu.Unlock()

	if host == "" {
		closeAllPairSockets()
	}
}

func pairRelayConfig() (string, int, int, bool) {
	pairMu.RLock()
	defer pairMu.RUnlock()

	return pairHost, pairPortBase, pairPortSpan, pairHost != "" && pairPortSpan > 0
}

// PairRelayActive dit si le relais par paire est arme.
func PairRelayActive() bool {
	_, _, _, on := pairRelayConfig()

	return on
}

// pairPortFor rend le port attribue a une paire de PID. Deterministe et symetrique : les
// deux appelants — celui qui repond aux URL de session et celui qui pousse l initiation de
// sonde — obtiennent le meme port sans rien se transmettre.
func pairPortFor(a, b uint64) (int, bool) {
	_, base, span, on := pairRelayConfig()
	if !on {
		return 0, false
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	// Melange simple et stable (FNV-1a sur les deux PID). Une collision ne fait que
	// partager un port entre deux paires ; la liste d adresses attendues ci-dessous la
	// rend inoffensive, le paquet de trop est refuse et journalise.
	h := uint64(14695981039346656037)
	for _, v := range [2]uint64{lo, hi} {
		for i := 0; i < 8; i++ {
			h ^= (v >> (8 * i)) & 0xFF
			h *= 1099511628211
		}
	}

	return base + int(h%uint64(span)), true
}

// pairSocket : un port, deux extremites. Aucune lecture du contenu.
type pairSocket struct {
	conn *net.UDPConn
	port int

	// Les deux PID que ce port sert. Fixes a l ouverture et jamais modifies : ils
	// identifient la paire dans les statistiques, et servent a detecter une collision de
	// port avec une AUTRE paire.
	pidLo, pidHi uint64

	mu       sync.Mutex
	attendus [2]string // les deux IP publiques admises sur ce port
	vus      [2]*net.UDPAddr
	dernier  time.Time

	// Comptage des paquets, pour savoir si une paire a VRAIMENT porte une partie. Une
	// capture du 2026-08-31 a montre 254 paquets entrants pour 104 relayes sans qu aucune
	// ligne de journal ne l indique : les compteurs cote serveur regardaient l appairage,
	// jamais le trafic.
	recus   uint64
	relayes uint64
	rejets  uint64
	remaps  uint64
	rejetVu map[string]uint64 // source refusee -> nombre de paquets
}

// PairRelayFor arme le port de la paire et rend l adresse a annoncer aux deux consoles.
//
// ipA et ipB sont les adresses publiques observees des deux joueurs : elles servent de
// liste d admission, pour qu un paquet venu d ailleurs ne puisse pas se glisser dans
// l appairage et detourner le flux.
func PairRelayFor(pidA, pidB uint64, ipA, ipB string) (string, int, bool) {
	host, _, _, on := pairRelayConfig()
	if !on || ipA == "" || ipB == "" {
		return "", 0, false
	}

	// L autorisation AVANT tout le reste : c est le seul passage par lequel entrent les deux
	// points d appel, donc le seul endroit ou l on peut garantir qu aucun autre appelant
	// ajoute plus tard ne contournera la liste. Les DEUX joueurs doivent y figurer — on ne
	// peut pas relayer un seul cote d une paire, et accepter sur un seul detournerait le
	// trafic de son partenaire, qui n a rien demande.
	if !RelayVolontaire(pidA) || !RelayVolontaire(pidB) {
		return "", 0, false
	}

	// Seulement pour ceux qui en ont besoin. Detourner une paire qui se joignait en direct,
	// c est lui ajouter un aller-retour par la France et lui faire courir le risque du
	// moindre defaut du relais — pour rien. Mesure du 2026-08-31 : arme pour tout le monde,
	// il a mis dehors des joueurs qui n avaient aucun probleme.
	//
	// Il suffit qu UN des deux echoue : le percage est une operation a deux, et le NAT strict
	// d un seul suffit a la faire rater.
	if !BesoinDeRelais(pidA) && !BesoinDeRelais(pidB) &&
		!RelayForce(pidA) && !RelayForce(pidB) {
		return "", 0, false
	}
	souhaite, ok := pairPortFor(pidA, pidB)
	if !ok {
		return "", 0, false
	}
	_, base, span, _ := pairRelayConfig()

	lo, hi := pidA, pidB
	if lo > hi {
		lo, hi = hi, lo
	}
	cle := [2]uint64{lo, hi}

	pairSocksMu.Lock()

	// Le hachage tape dans 900 ports : deux paires differentes finissent regulierement sur
	// le meme. L ancien code reutilisait alors la socket de l autre paire et ECRASAIT sa
	// liste d adresses attendues, expulsant sur-le-champ deux joueurs en pleine partie. La
	// probabilite est de l ordre de 20% a vingt paires simultanees et de 60% a quarante —
	// donc d autant plus forte qu il y a de monde, ce qui colle aux paires muettes du
	// 2026-08-31. Le commentaire de pairPortFor disait la collision inoffensive ; elle ne
	// l est pas, car c est l ARRIVANT qui gagne et l occupant qui saute.
	port, existe := pairPorts[cle]
	if !existe {
		port = -1
		for i := 0; i < 8; i++ {
			cand := base + (souhaite-base+i)%span
			occupant := pairSocks[cand]
			if occupant == nil || (occupant.pidLo == lo && occupant.pidHi == hi) {
				port = cand

				break
			}
		}
		if port < 0 {
			pairSocksMu.Unlock()
			fmt.Printf("[relais-paire] collision non resolue pour pid=%d/%d — laisse en direct\n", pidA, pidB)

			return "", 0, false
		}
	}

	ps := pairSocks[port]
	if ps == nil {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
		if err != nil {
			pairSocksMu.Unlock()
			fmt.Printf("[relais-paire] ecoute UDP :%d impossible: %v\n", port, err)

			return "", 0, false
		}
		ps = &pairSocket{conn: conn, port: port, pidLo: lo, pidHi: hi}
		pairSocks[port] = ps
		pairPorts[cle] = port
		pairTotal.PairesOuvertes++
		go ps.boucle()
		deplace := ""
		if port != souhaite {
			deplace = fmt.Sprintf(" (deplace depuis :%d, collision)", souhaite)
		}
		fmt.Printf("[relais-paire] :%d ouvert pour pid=%d <-> pid=%d (%s, %s)%s\n", port, pidA, pidB, ipA, ipB, deplace)
	} else if ps.pidLo != lo || ps.pidHi != hi {
		// Ne JAMAIS reutiliser la socket d une autre paire : c est precisement ce qui
		// expulsait l occupant.
		pairSocksMu.Unlock()
		fmt.Printf("[relais-paire] :%d appartient a pid=%d/%d — pid=%d/%d laisse en direct\n",
			port, ps.pidLo, ps.pidHi, pidA, pidB)

		return "", 0, false
	}
	if !pairReaping {
		pairReaping = true
		go reapPairSockets()
	}
	pairSocksMu.Unlock()

	// Les adresses attendues sont rangees DANS L ORDRE DES PID, comme pairPortFor range
	// deja lo/hi pour que la paire tombe toujours sur le meme port.
	//
	// Sans ce tri, la liste s inversait d un appel a l autre : GetSessionURLs appelle avec
	// (visiteur, hote), InitiateProbe avec (appelant, cible) — souvent (hote, visiteur). Les
	// places deja occupees, elles, ne bougeaient pas : le joueur assis en place 2 devenait
	// l occupant attendu de la place 1, et le suivi de port le deplacait. Les deux joueurs
	// finissaient dans la MEME place, l autre restait vide, et la paire ne relayait plus
	// rien. Mesure du 2026-08-31 : 3 paires sur 17 a relayes=0, rejets=0 — invisibles sans
	// les compteurs, et fatales puisque le candidat direct est deja remplace.
	attA, attB := ipA, ipB
	if pidA > pidB {
		attA, attB = ipB, ipA
	}

	ps.mu.Lock()
	ps.attendus = [2]string{attA, attB}
	ps.dernier = time.Now()
	if ps.rejetVu == nil {
		ps.rejetVu = map[string]uint64{}
	}
	ps.mu.Unlock()

	// Seulement ici, au retour reussi : les sorties precedentes (desarme, sans besoin,
	// collision) laissent la paire en DIRECT, et leurs verdicts sont de vraies mesures.
	NoterPaireRelayee(pidA, pidB)

	return host, port, true
}

// boucle fait traverser tout ce qui arrive, dans les deux sens.
func (ps *pairSocket) boucle() {
	buf := make([]byte, 2048)
	for {
		n, src, err := ps.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket fermee par le faucheur
		}

		ps.mu.Lock()
		ps.recus++
		ps.mu.Unlock()

		dst := ps.apparier(src)
		if dst == nil {
			continue
		}
		ps.mu.Lock()
		ps.relayes++
		ps.mu.Unlock()
		if _, err := ps.conn.WriteToUDP(buf[:n], dst); err != nil {
			fmt.Printf("[relais-paire] :%d renvoi %s -> %s echoue: %v\n", ps.port, src, dst, err)
		}
	}
}

// memeReseau dit si deux adresses IPv4 partagent leur /24.
//
// L admission comparait l IP EXACTE, et l IP attendue vient de la connexion NEX (le
// WebSocket de controle) alors que le jeu emet son UDP par une autre sortie. Sous CGNAT les
// deux diffèrent : mesure du 2026-08-31, une paire attendait 45.186.208.118 et recevait de
// 45.186.208.68 — 88 paquets refuses, zero relaye, la partie morte.
//
// Le /24 est un compromis assume : il couvre les pools CGNAT sans ouvrir le port a tout
// Internet. Le port reste dedie a UNE paire, tire au hasard parmi 900 et ferme apres deux
// minutes de silence, donc ce qu on relache ici reste etroit.
func memeReseau(a, b string) bool {
	ipA, ipB := net.ParseIP(a), net.ParseIP(b)
	if ipA == nil || ipB == nil {
		return false
	}
	v4A, v4B := ipA.To4(), ipB.To4()
	if v4A == nil || v4B == nil {
		return false // IPv6 : pas de /24 qui ait du sens ici
	}

	return v4A[0] == v4B[0] && v4A[1] == v4B[1] && v4A[2] == v4B[2]
}

// admis dit si une source peut prendre la place i : IP exacte, ou meme /24 (CGNAT).
func admis(attendu, src string) bool {
	return attendu == src || memeReseau(attendu, src)
}

// apparier rend l autre extremite pour un paquet donne, en apprenant les endpoints au fil
// de l eau et en refusant tout ce qui ne vient pas des deux adresses attendues.
func (ps *pairSocket) apparier(src *net.UDPAddr) *net.UDPAddr {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.dernier = time.Now()

	for i, vu := range ps.vus {
		if vu != nil && vu.Port == src.Port && vu.IP.Equal(src.IP) {
			return ps.vus[1-i]
		}
	}
	// Nouvel endpoint : il n est accepte que s il vient d une des deux IP attendues, et
	// seulement pour la place encore libre. Sans ce garde-fou, n importe quel scanner
	// s inserant sur le port detournerait la partie de deux joueurs.
	for i, ip := range ps.attendus {
		if admis(ip, src.IP.String()) && ps.vus[i] == nil {
			ps.vus[i] = src
			fmt.Printf("[relais-paire] :%d extremite %d = %s\n", ps.port, i+1, src)

			return ps.vus[1-i]
		}
	}

	// Meme IP, place DEJA prise, port different : le NAT du joueur a remappe en cours de
	// session. L ancien code jetait ces paquets pour toujours — or c est precisement le
	// joueur au NAT strict que le relais existe pour servir. On suit le nouveau port.
	//
	// Uniquement quand les deux IP attendues DIFFERENT : si les deux joueurs partagent une
	// IP publique (meme foyer, CGNAT), le port est le seul discriminant et le suivre
	// melangerait les deux flux.
	// Le remappage n est sur que si les deux attendus sont distinguables PAR RESEAU. Depuis
	// que l admission accepte le /24, deux joueurs du meme bloc ne le sont plus : suivre le
	// port melangerait leurs deux flux dans la meme partie.
	if !memeReseau(ps.attendus[0], ps.attendus[1]) && ps.attendus[0] != ps.attendus[1] {
		for i, ip := range ps.attendus {
			if admis(ip, src.IP.String()) && ps.vus[i] != nil {
				ancien := ps.vus[i]
				ps.vus[i] = src
				ps.remaps++
				fmt.Printf("[relais-paire] :%d extremite %d remappee %s -> %s (NAT symetrique)\n",
					ps.port, i+1, ancien, src)

				return ps.vus[1-i]
			}
		}
	}

	// Refus. On garde QUI a ete refuse : sans cette ligne, un relais qui jette la moitie du
	// trafic est indiscernable d un relais qui marche.
	ps.rejets++
	if ps.rejetVu != nil {
		ps.rejetVu[src.String()]++
	}

	return nil
}

func (ps *pairSocket) inactifDepuis(d time.Duration) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	return time.Since(ps.dernier) > d
}

// reapPairSockets ferme les ports dont la paire ne parle plus. Sans lui, une soiree
// chargee laisserait un socket ouvert par partie jouee depuis le demarrage.
func reapPairSockets() {
	tour := 0
	for {
		time.Sleep(30 * time.Second)
		tour++

		pairSocksMu.Lock()
		for port, ps := range pairSocks {
			if ps.inactifDepuis(pairRelayTTL) {
				ps.conn.Close()
				delete(pairSocks, port)
				delete(pairPorts, [2]uint64{ps.pidLo, ps.pidHi})
				// Bilan a la fermeture : c est la SEULE trace qui dise si la paire a
				// porte une partie ou si elle a jete le trafic. Les refus sont
				// detailles par source, parce que « qui a ete refuse » est la
				// question, pas « combien ».
				ps.mu.Lock()
				recus, relayes, rejets, remaps := ps.recus, ps.relayes, ps.rejets, ps.remaps
				detail := ""
				for src, n := range ps.rejetVu {
					detail += fmt.Sprintf(" %s×%d", src, n)
				}
				attendus := ps.attendus
				ps.mu.Unlock()

				pairTotal.Recus += recus
				pairTotal.Relayes += relayes
				pairTotal.Rejets += rejets
				pairTotal.Remaps += remaps
				if relayes == 0 {
					pairTotal.PairesMuettes++
				}

				fmt.Printf("[relais-paire] :%d ferme — recus=%d relayes=%d rejets=%d remaps=%d (attendus %s / %s)%s\n",
					port, recus, relayes, rejets, remaps, attendus[0], attendus[1], detail)
			}
		}
		// Bilan periodique. Le bilan par paire n arrive qu a sa fermeture, jusqu a deux
		// minutes et demie apres qu elle s est tue : pendant un essai, c est trop tard
		// pour decider quoi que ce soit. Une ligne toutes les trois minutes donne l etat
		// pendant qu il se passe quelque chose, et elle existe meme si le tableau de bord
		// est tombe.
		if tour%6 == 0 {
			vivantes := len(pairSocks)
			enCours := PairTotaux{}
			for _, ps := range pairSocks {
				ps.mu.Lock()
				enCours.Recus += ps.recus
				enCours.Relayes += ps.relayes
				enCours.Rejets += ps.rejets
				ps.mu.Unlock()
			}
			fmt.Printf("[relais-paire] bilan — vivantes=%d | en cours recus=%d relayes=%d rejets=%d | cumul ouvertes=%d muettes=%d relayes=%d rejets=%d\n",
				vivantes, enCours.Recus, enCours.Relayes, enCours.Rejets,
				pairTotal.PairesOuvertes, pairTotal.PairesMuettes, pairTotal.Relayes, pairTotal.Rejets)
		}

		vide := len(pairSocks) == 0
		if vide {
			pairReaping = false
		}
		pairSocksMu.Unlock()

		if vide {
			return
		}
	}
}

func closeAllPairSockets() {
	pairSocksMu.Lock()
	defer pairSocksMu.Unlock()

	for port, ps := range pairSocks {
		ps.conn.Close()
		delete(pairSocks, port)
	}
	// Sinon une paire desarmee puis rearmee retrouverait un port dont la socket n existe
	// plus, et le registre grandirait sans fin.
	pairPorts = map[[2]uint64]int{}
	fmt.Printf("[relais-paire] desarme, tous les ports fermes\n")
}

// RelayStations repointe des stations vers le relais, en conservant tout le reste — CID,
// PID, RVCID, natf, natm. Ce sont eux qui permettent a la Pia du pair de MONTER la session
// une fois le premier paquet passe ; les perdre redonne un 2618-0502.
// relayPointStation rend une copie d une station pointant vers le relais. Adresse, port, et
// Pa s il etait deja la — RIEN d autre. Surtout pas natf/natm : la Pia du pair les lit pour
// choisir comment sonder, et 0/0 ne veut pas dire « ouvert », il veut dire « non renseigne ».
// Un seul point de verite, pour que les deux cotes (GetSessionURLs et InitiateProbe) ne
// puissent pas diverger.
func relayPointStation(u *StationURL, host string, port int) *StationURL {
	if u == nil {
		return nil
	}
	c := u.Copy()
	c.Set("address", host)
	c.SetInt("port", port)
	if c.Get("Pa") != "" {
		c.Set("Pa", host)
	}

	return c
}

// RelayStations repointe vers le relais LA SEULE station publique, et rend toutes les autres
// telles quelles.
//
// L ancienne version repointait TOUTES les stations, donc leur donnait la meme adresse ET le
// meme port : le visiteur recevait deux candidats identiques, et perdait au passage le CID de
// l hote que porte la station LAN — celui dont sa Pia a besoin pour MONTER la session apres
// le percage. La branche relay-fallback avait deja mesure la meme chose en production
// (472c9d0) : le visiteur appelle EndParticipation des reception, avant meme de sonder. C est
// l explication la plus probable des 85% de verdicts FAILED du 2026-08-31.
//
// La publique est reperee par ADRESSE (selectStations), pas par la presence de Pa : Splatoon 2
// tourne en LegacyPiaConfig, qui n envoie jamais Pa, et relayedFor s applique aussi aux
// chemins NON pontes ou aucune station n en porte. Un test sur Pa n y verrait rien a changer.
func RelayStations(urls []*StationURL, host string, port int) []*StationURL {
	_, public := selectStations(urls)
	if public == nil || isPrivateIP(public.Get("address")) {
		// Pas de candidat public a repointer : mieux vaut laisser la liste intacte et
		// rester en direct que rendre une forme qu on ne sait pas construire.
		fmt.Printf("[relais-paire] aucun candidat public a repointer — laisse en direct\n")

		return urls
	}

	out := make([]*StationURL, 0, len(urls))
	for _, u := range urls {
		if u == nil {
			continue
		}
		if u != public {
			// Le meme pointeur, pas une copie : la facon la plus forte de dire
			// « intacte », et la plus simple a verifier dans un test.
			out = append(out, u)

			continue
		}
		out = append(out, relayPointStation(u, host, port))
	}

	return out
}

// PublicIPOf rend l adresse publique observee d une connexion, ou "" si elle est privee ou
// illisible : c est ce qui alimente la liste d admission d un port de paire.
func PublicIPOf(c *Connection) string {
	if c == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(c.RemoteAddr)
	if err != nil || host == "" || isPrivateIP(host) {
		return ""
	}

	return host
}
