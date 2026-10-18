package nex

import "time"

// Trace des connexions qui viennent de se fermer.
//
// Un verdict de percage (ReportNATTraversalResult) nomme le pair par son Connection.ID. Quand
// le pair s est deconnecte entre le sondage et le verdict, FindConnectionByID ne le trouve
// plus : logNATResult ne savait alors ni son PID (donc pas si la paire etait passee par le
// relais) ni son natm (donc pas la classe Pia). Prod du 17/10 : a 01:24:16Z et 01:59:13Z,
// deux paires ouvertes sur le relais, dont l hote s etait deconnecte moins d une seconde a
// six secondes avant le verdict, ont ete comptees comme echecs DIRECTS de classe « ? ».
//
// La trace garde, pour chaque connexion fermee, ce que le verdict doit savoir d elle, pendant
// relayVuTTL : au-dela, PaireRelayee ne dirait plus rien de la paire de toute facon.

// connPartie : ce qu un verdict de percage doit savoir d un pair deja parti.
type connPartie struct {
	pid  uint64
	natm int    // natmStation au moment de la fermeture
	ip   string // PublicIPOf au moment de la fermeture
	le   time.Time
}

// partisMax borne la trace : au-dela (une rafale de connexions fermees en moins de
// relayVuTTL), les suivantes ne sont pas gardees, et leur verdict est traite comme avant.
const partisMax = 1 << 16

// noterPartie garde la trace d une connexion qui vient d etre retiree (unregisterConnection).
func (e *Endpoint) noterPartie(c *Connection) {
	if c == nil || c.ID == 0 {
		return
	}
	trace := connPartie{pid: c.PID, natm: natmStation(c), ip: PublicIPOf(c), le: time.Now()}

	e.partisMu.Lock()
	defer e.partisMu.Unlock()
	if e.partis == nil {
		e.partis = map[uint32]connPartie{}
	}
	// Menage au plus une fois par minute, comme les autres registres du relais.
	if trace.le.Sub(e.partisMenage) > time.Minute {
		for id, p := range e.partis {
			if trace.le.Sub(p.le) > relayVuTTL {
				delete(e.partis, id)
			}
		}
		e.partisMenage = trace.le
	}
	if len(e.partis) >= partisMax {
		return
	}
	e.partis[c.ID] = trace
}

// connexionPartie rend la trace d une connexion fermee depuis moins de relayVuTTL.
func (e *Endpoint) connexionPartie(id uint32) (connPartie, bool) {
	e.partisMu.Lock()
	defer e.partisMu.Unlock()
	p, ok := e.partis[id]
	if !ok || time.Since(p.le) > relayVuTTL {
		return connPartie{}, false
	}

	return p, true
}
