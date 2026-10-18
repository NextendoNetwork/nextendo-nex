package nex

// Points mineurs de la relecture r2 (securite, coeur) :
//   - TestSessionsSeulBornees : chaque 0x6D.0x28 sans candidat ouvrait une session de plus,
//     sans borne tant que la connexion restait ouverte (7391 au nom d'un seul client pendant
//     la force brute) ;
//   - TestMdpPurgeBornee : au-dela de 4096 cles, chaque nouvelle cle refusee parcourait toute
//     la table des seaux sous m.mu.
// PID fictifs 18009xxxxx, adresses de documentation (192.0.2.0/24, 198.51.100.0/24).

import (
	"fmt"
	"testing"
	"time"
)

// seulDe : sessions ou pid est le seul participant.
func (b *bancArene) seulDe(pid uint64) (n int, gids map[uint32]bool) {
	b.mm.mu.Lock()
	defer b.mm.mu.Unlock()
	gids = map[uint32]bool{}
	for gid, g := range b.mm.gatherings {
		if len(g.participants) == 1 && g.participants[0] == pid {
			n++
			gids[gid] = true
		}
	}
	return n, gids
}

func TestSessionsSeulBornees(t *testing.T) {
	_ = horlogeMdp(t)
	b := nouveauBancArene(t)
	hote := newTestConn(b.ep, pidHoteArene, "192.0.2.1:1")
	invite := newTestConn(b.ep, pidBonMdp, "192.0.2.2:1")
	// Une arene ou quelqu'un d'autre est present : jamais fermee par la borne.
	arene := b.creer(hote, 0x51, mdpArene)
	if r := b.rejoindre(invite, arene.ID, mdpArene); r == nil || r.IsError {
		t.Fatalf("jonction : %+v", r)
	}
	spam := newTestConn(b.ep, 1800900310, "192.0.2.10:1")
	var derniere uint32
	for i := 0; i < 500; i++ {
		derniere = b.auto28(spam, 0x0100_0000+uint32(i), "").ID
		if i%7 == 0 {
			// L'hote de l'arene ouvre aussi des sessions a lui seul.
			b.auto28(hote, 0x0200_0000+uint32(i), "")
		}
	}
	n, gids := b.seulDe(spam.PID)
	nh, _ := b.seulDe(hote.PID)
	t.Logf("500 x 0x6D.0x28 sans candidat : %d session(s) ou le client est seul, hote : %d, arene partagee gardee = %v",
		n, nh, len(b.participants(arene.ID)) == 2)
	if n > sessionsSeulMax || nh > sessionsSeulMax {
		t.Fatalf("SESSIONS SANS BORNE : %d (client) et %d (hote) sessions a un seul participant, attendu au plus %d", n, nh, sessionsSeulMax)
	}
	if !gids[derniere] {
		t.Fatalf("la session la plus recente (%d) a ete fermee", derniere)
	}
	if p := b.participants(arene.ID); len(p) != 2 {
		t.Fatalf("arene partagee touchee : participants %v", p)
	}
}

func TestMdpPurgeBornee(t *testing.T) {
	maintenant := horlogeMdp(t)
	b := nouveauBancArene(t)
	// 5000 seaux non vides : aucune purge ne peut rien retirer.
	b.mm.mu.Lock()
	for i := 0; i < 5000; i++ {
		b.mm.mdpNoterRefus(newTestConn(b.ep, 1800910000+uint64(i), fmt.Sprintf("198.51.100.%d:%d", i%250, i)))
	}
	avant := b.mm.mdpPurges
	// 3000 nouveaux PID refuses dans la meme seconde.
	for i := 0; i < 3000; i++ {
		b.mm.mdpNoterRefus(newTestConn(b.ep, 1800920000+uint64(i), "192.0.2.99:1"))
	}
	pendant := b.mm.mdpPurges - avant
	b.mm.mu.Unlock()
	t.Logf("3000 nouvelles cles au-dela de %d : %d parcours de la table", mdpSeauxPurge, pendant)
	if pendant > 1 {
		t.Fatalf("PURGE A CHAQUE CLE : %d parcours de la table pour 3000 nouvelles cles dans la meme seconde", pendant)
	}
	// La purge oublie toujours les seaux vides, une fois l'ecart passe (48 h : le seau de
	// 192.0.2.99, 3000 refus, se vide de 1 par 30 s).
	*maintenant = maintenant.Add(48 * time.Hour)
	b.mm.mu.Lock()
	b.mm.mdpNoterRefus(newTestConn(b.ep, 1800930000, "192.0.2.98:1"))
	reste := len(b.mm.mdpSeaux)
	b.mm.mu.Unlock()
	if reste > 2 {
		t.Fatalf("seaux vides non oublies apres 48 h : %d cles", reste)
	}
}
