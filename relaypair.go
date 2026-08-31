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
	pairReaping bool
)

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
	port, ok := pairPortFor(pidA, pidB)
	if !ok {
		return "", 0, false
	}

	pairSocksMu.Lock()
	ps := pairSocks[port]
	if ps == nil {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
		if err != nil {
			pairSocksMu.Unlock()
			fmt.Printf("[relais-paire] ecoute UDP :%d impossible: %v\n", port, err)

			return "", 0, false
		}
		ps = &pairSocket{conn: conn, port: port}
		pairSocks[port] = ps
		go ps.boucle()
		fmt.Printf("[relais-paire] :%d ouvert pour pid=%d <-> pid=%d (%s, %s)\n", port, pidA, pidB, ipA, ipB)
	}
	if !pairReaping {
		pairReaping = true
		go reapPairSockets()
	}
	pairSocksMu.Unlock()

	ps.mu.Lock()
	ps.attendus = [2]string{ipA, ipB}
	ps.dernier = time.Now()
	if ps.rejetVu == nil {
		ps.rejetVu = map[string]uint64{}
	}
	ps.mu.Unlock()

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
		if ip == src.IP.String() && ps.vus[i] == nil {
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
	if ps.attendus[0] != ps.attendus[1] {
		for i, ip := range ps.attendus {
			if ip == src.IP.String() && ps.vus[i] != nil {
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
	for {
		time.Sleep(30 * time.Second)

		pairSocksMu.Lock()
		for port, ps := range pairSocks {
			if ps.inactifDepuis(pairRelayTTL) {
				ps.conn.Close()
				delete(pairSocks, port)
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

				fmt.Printf("[relais-paire] :%d ferme — recus=%d relayes=%d rejets=%d remaps=%d (attendus %s / %s)%s\n",
					port, recus, relayes, rejets, remaps, attendus[0], attendus[1], detail)
			}
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
	fmt.Printf("[relais-paire] desarme, tous les ports fermes\n")
}

// RelayStations repointe des stations vers le relais, en conservant tout le reste — CID,
// PID, RVCID, natf, natm. Ce sont eux qui permettent a la Pia du pair de MONTER la session
// une fois le premier paquet passe ; les perdre redonne un 2618-0502.
func RelayStations(urls []*StationURL, host string, port int) []*StationURL {
	out := make([]*StationURL, 0, len(urls))
	for _, u := range urls {
		if u == nil {
			continue
		}
		c := u.Copy()
		c.Set("address", host)
		c.SetInt("port", port)
		if c.Get("Pa") != "" {
			c.Set("Pa", host)
		}
		out = append(out, c)
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
