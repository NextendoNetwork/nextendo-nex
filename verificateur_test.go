package nex

import (
	"errors"
	"sync"
)

// Vérificateur de CONNECT des tests du cœur public. Il ne vérifie rien et n'existe que pour
// tester le transport ; un serveur sans vérificateur refuse tout CONNECT secure
// (verification_publique.go). Il accepte deux formes :
//   - CONNECT vide : l'identité que le test a déclarée pour cette connexion (identiteDeTest,
//     clé = adresse de la connexion de test), sinon refus ;
//   - charge au format des tests (chargeConnectTest : PID, CID et check en clair) : réponse
//     [u32 4][u32 check+1], comme celle qu'attend une console.
var identitesDeTest sync.Map // adresse « ip:port » -> PID

func identiteDeTest(addr string, pid uint64) { identitesDeTest.Store(addr, pid) }

// chargeConnectTest : charge utile d'un CONNECT secure au format des tests.
func chargeConnectTest(s *Settings, pid uint64, cid, check uint32) []byte {
	o := NewStreamOut(s)
	o.PID(pid)
	o.U32(cid)
	o.U32(check)
	return o.Bytes()
}

func init() {
	InstallerVerificateurConnexion(func(c *Connection, payload []byte) ([]byte, IdentiteVerifiee, error) {
		if len(payload) == 0 {
			v, ok := identitesDeTest.Load(c.RemoteAddr)
			if !ok {
				return nil, IdentiteVerifiee{}, errors.New("test : aucune identite enregistree pour cette adresse")
			}
			return []byte{}, IdentiteVerifiee{PID: v.(uint64)}, nil
		}
		if len(payload) != c.Settings.PIDSize+8 {
			return nil, IdentiteVerifiee{}, errors.New("test : charge illisible")
		}
		in := NewStreamIn(payload, c.Settings)
		pid := in.PID()
		cid := in.U32()
		check := in.U32()
		out := NewStreamOut(c.Settings)
		out.U32(4)
		out.U32(check + 1)
		return out.Bytes(), IdentiteVerifiee{PID: pid, CID: cid, Ticket: true}, nil
	})
}
