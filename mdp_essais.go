package nex

import (
	"net"
	"strconv"
	"time"
)

// Essais de mot de passe d'arene (JoinMatchmakeSessionWithParam 0x6D.0x27, et
// AutoMatchmakeWithParam_Postpone 0x6D.0x28 par autoMdpCompatible), bornes.
//
// Sans borne, un client qui boucle sur 0x6D.0x27 trouve le mot de passe d'une arene : chaque
// essai recoit une reponse, et seul un mauvais mot de passe rend 0x800300D5. Le jeu n'accepte
// que des mots de passe courts (8 caracteres au plus, 0xd0600 appele en 0x3708b78 ; mot de
// passe d'arene = clavier de type 8, 0x3392038). Mesure de la relecture r1 : « 7391 » trouve
// en 7392 essais, plus de 130 000 essais/s dans le gestionnaire seul.
//
// Regle : un seau qui fuit par PID et un par adresse IP. Chaque refus ajoute 1 au seau du PID
// et a celui de son IP ; le seau se vide de 1 par periode (mdpFuitePID, mdpFuiteIP). Un essai
// n'est compare que si les deux seaux ont encore une place ; sinon il recoit 0x800300C9 SANS
// comparaison (« Impossible de rejoindre cette arene. », rien ne dit s'il a touche juste ;
// avant : 0x800300D5, et le joueur qui tapait le bon code lisait « Le code d'acces est
// incorrect. »). Un joueur qui se trompe 1 ou 2 fois n'est jamais gene ;
// apres 5 refus de suite, un essai toutes les 2 minutes. Par IP : 20 refus, puis un essai
// toutes les 30 s (plusieurs comptes derriere une meme IP). Debit maximal : 720 essais par
// jour et par PID, 2880 par IP, au lieu de plus de 10^10.
//
// Journal : une ligne quand un seau se remplit (debut d'une serie freinee), aucune pour les
// essais freines (avant : une ligne par refus, une campagne remplissait le journal du
// conteneur, 10 Mio x 3, et effacait ses propres traces).
var (
	mdpCapPID   = 5.0
	mdpFuitePID = 2 * time.Minute
	mdpCapIP    = 20.0
	mdpFuiteIP  = 30 * time.Second
	mdpHorloge  = time.Now
)

const (
	mdpSeauxPurge = 4096  // au-dela, les seaux vides sont oublies
	mdpSeauxMax   = 65536 // au-dela, une nouvelle cle n'est plus suivie (son autre cle l'est)
	// mdpPurgeEcart : une purge au plus par periode. Avant (relecture r2) : au-dela de 4096
	// cles, CHAQUE nouvelle cle parcourait toute la table (jusqu'a 65536 entrees) sous m.mu,
	// le verrou global du matchmaking. Maintenant un parcours par seconde au plus, quel que
	// soit le nombre de nouveaux PID ou IP refuses.
	mdpPurgeEcart = time.Second
)

type mdpSeau struct {
	niveau float64
	maj    time.Time
	fuite  time.Duration
}

// vider applique la fuite jusqu'a maintenant.
func (s *mdpSeau) vider(maintenant time.Time) {
	if d := maintenant.Sub(s.maj); d > 0 {
		s.niveau -= float64(d) / float64(s.fuite)
		if s.niveau < 0 {
			s.niveau = 0
		}
	}
	s.maj = maintenant
}

type mdpCle struct {
	cle   string
	cap   float64
	fuite time.Duration
}

// mdpCles : les seaux d'un demandeur (PID, puis IP de sa connexion).
func mdpCles(conn *Connection) []mdpCle {
	cles := []mdpCle{{cle: "pid:" + strconv.FormatUint(conn.PID, 10), cap: mdpCapPID, fuite: mdpFuitePID}}
	ip := conn.RemoteAddr
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	if ip != "" {
		cles = append(cles, mdpCle{cle: "ip:" + ip, cap: mdpCapIP, fuite: mdpFuiteIP})
	}
	return cles
}

// mdpSeauDe (sous m.mu) : le seau de cette cle, cree au besoin (nil si la table est pleine).
func (m *Matchmaking) mdpSeauDe(k mdpCle, maintenant time.Time) *mdpSeau {
	if m.mdpSeaux == nil {
		m.mdpSeaux = map[string]*mdpSeau{}
	}
	s := m.mdpSeaux[k.cle]
	if s == nil {
		if len(m.mdpSeaux) >= mdpSeauxPurge && (m.mdpPurges == 0 || maintenant.Sub(m.mdpPurgeA) >= mdpPurgeEcart) {
			m.mdpPurgeA = maintenant
			m.mdpPurges++
			for c, v := range m.mdpSeaux {
				if v.vider(maintenant); v.niveau == 0 {
					delete(m.mdpSeaux, c)
				}
			}
		}
		if len(m.mdpSeaux) >= mdpSeauxMax {
			return nil
		}
		s = &mdpSeau{maj: maintenant, fuite: k.fuite}
		m.mdpSeaux[k.cle] = s
	}
	s.vider(maintenant)
	return s
}

// mdpEssaiPermis (sous m.mu) : le mot de passe de cet essai peut-il etre compare.
func (m *Matchmaking) mdpEssaiPermis(conn *Connection) bool {
	maintenant := mdpHorloge()
	for _, k := range mdpCles(conn) {
		if m.mdpSeaux == nil {
			return true
		}
		s := m.mdpSeaux[k.cle]
		if s == nil {
			continue
		}
		if s.vider(maintenant); s.niveau+1 > k.cap {
			return false
		}
	}
	return true
}

// mdpNoterRefus (sous m.mu) : un refus de plus. Rend vrai si un seau vient de se remplir
// (debut d'une serie freinee, a journaliser).
func (m *Matchmaking) mdpNoterRefus(conn *Connection) bool {
	maintenant := mdpHorloge()
	plein := false
	for _, k := range mdpCles(conn) {
		s := m.mdpSeauDe(k, maintenant)
		if s == nil {
			continue
		}
		s.niveau++
		if s.niveau+1 > k.cap {
			plein = true
		}
	}
	return plein
}
