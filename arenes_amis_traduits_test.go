package nex

import "testing"

// demandesArenesAmis compte les identifiants traduits NSA -> PID (journalises par
// findArenesAmis) : un NSA connu de l authentification compte, un PID ou un NSA inconnu non,
// et un identifiant repete une seule fois.
func TestArenesAmisCompteLesNSATraduits(t *testing.T) {
	const nsa uint64 = 1810090000000000031
	RememberLoginName("1810090000000000031", 1800900231)
	demandes, traduits := demandesArenesAmis([]uint64{nsa, nsa, 1800900232, 1810090000000000039, 0})
	if traduits != 1 {
		t.Fatalf("%d identifiant(s) traduit(s), attendu 1", traduits)
	}
	if got := demandes[1800900231]; len(got) != 1 || got[0] != nsa {
		t.Fatalf("PID 1800900231 : %v, attendu [nsa]", got)
	}
	if got := demandes[1800900232]; len(got) != 1 || got[0] != 1800900232 {
		t.Fatalf("PID demande directement : %v", got)
	}
}
