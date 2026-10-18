package nex

import (
	"errors"
	"testing"
)

// TestSansVerificateurConnectRefuse : la version publique ne donne aucune identité qu'elle
// n'a pas vérifiée. Sans vérificateur installé, un CONNECT secure est refusé, vide ou non.
func TestSansVerificateurConnectRefuse(t *testing.T) {
	ancien := verificateurConnexion.Load()
	InstallerVerificateurConnexion(nil)
	defer verificateurConnexion.Store(ancien)

	s := testSettings()
	ep := NewEndpoint(s)
	ep.SetSecureAccount("mot-de-passe-de-test", 2)
	identiteDeTest("192.0.2.70:1", 1899000070)
	c := NewConnection(ep, "192.0.2.70:1", func([]byte) {})
	for _, charge := range [][]byte{nil, chargeConnectTest(s, 1899000070, 1, 2)} {
		if _, err := c.processLoginRequest(charge); !errors.Is(err, ErrVerificationNonIncluse) {
			t.Fatalf("CONNECT secure sans vérificateur : erreur %v, attendu un refus", err)
		}
	}
	// Endpoint non secure (auth) : inchangé, accepté sans identité.
	auth := NewConnection(NewEndpoint(s), "192.0.2.70:2", func([]byte) {})
	if _, err := auth.processLoginRequest(nil); err != nil {
		t.Fatalf("auth : %v", err)
	}
}

// TestVerificateurSansPIDRefuse : un vérificateur qui n'établit aucun PID ne fait rien accepter.
func TestVerificateurSansPIDRefuse(t *testing.T) {
	ancien := verificateurConnexion.Load()
	InstallerVerificateurConnexion(func(*Connection, []byte) ([]byte, IdentiteVerifiee, error) {
		return nil, IdentiteVerifiee{}, nil
	})
	defer verificateurConnexion.Store(ancien)
	ep := NewEndpoint(testSettings())
	ep.SetSecureAccount("mot-de-passe-de-test", 2)
	c := NewConnection(ep, "192.0.2.71:1", func([]byte) {})
	if _, err := c.processLoginRequest(nil); err == nil {
		t.Fatal("vérificateur sans PID : CONNECT accepté")
	}
}
