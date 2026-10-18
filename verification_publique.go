package nex

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// Vérification des CONNECT secure : NON INCLUSE dans la version publique du cœur.
//
// Un CONNECT sur un endpoint secure doit établir QUI se connecte. La version de production
// vérifie ici les tickets et l'identité des consoles ; cette vérification ne fait pas partie du
// dépôt public. Il n'en garde que le point d'entrée : l'exploitant installe son propre
// vérificateur (InstallerVerificateurConnexion). Sans vérificateur, tout CONNECT secure est
// refusé : la version publique ne donne jamais une identité qu'elle n'a pas vérifiée.

// IdentiteVerifiee : ce qu'un vérificateur établit pour un CONNECT secure accepté.
type IdentiteVerifiee struct {
	PID        uint64
	CID        uint32
	CleSession []byte
	// Ticket : l'identité vient d'un ticket présenté au CONNECT (journal, titres).
	Ticket bool
}

// VerificateurConnexion vérifie la charge utile d'un CONNECT secure. Il rend la réponse à
// renvoyer à la console et l'identité établie, ou une erreur : le CONNECT est alors refusé.
type VerificateurConnexion func(c *Connection, payload []byte) (reponse []byte, id IdentiteVerifiee, err error)

var verificateurConnexion atomic.Pointer[VerificateurConnexion]

// ErrVerificationNonIncluse : aucun vérificateur installé, le CONNECT secure est refusé.
var ErrVerificationNonIncluse = errors.New("connect: verification des tickets et de l'identite non incluse dans la version publique du coeur (aucun verificateur installe)")

// InstallerVerificateurConnexion installe le vérificateur des CONNECT secure (nil : aucun, tout
// CONNECT secure est refusé). À appeler au démarrage, avant de servir.
func InstallerVerificateurConnexion(f VerificateurConnexion) {
	if f == nil {
		verificateurConnexion.Store(nil)
		return
	}
	verificateurConnexion.Store(&f)
}

// verifierConnexion applique le vérificateur installé. Sans vérificateur, ou si aucun PID
// n'est établi : refus.
func verifierConnexion(c *Connection, payload []byte) ([]byte, IdentiteVerifiee, error) {
	f := verificateurConnexion.Load()
	if f == nil {
		return nil, IdentiteVerifiee{}, ErrVerificationNonIncluse
	}
	reponse, id, err := (*f)(c, payload)
	if err != nil {
		return nil, IdentiteVerifiee{}, err
	}
	if id.PID == 0 {
		return nil, IdentiteVerifiee{}, fmt.Errorf("connect: le verificateur n'a etabli aucun PID")
	}
	if reponse == nil {
		reponse = []byte{}
	}
	return reponse, id, nil
}
