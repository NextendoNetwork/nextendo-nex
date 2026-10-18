package nex

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Adresses de documentation (RFC 5737) uniquement : aucune adresse de joueur.
const (
	plageA = "203.0.113.0-203.0.113.255"
	plageB = "198.51.100.0-198.51.100.255"
	ipA1   = "203.0.113.10"
	ipA2   = "203.0.113.20"
	ipB1   = "198.51.100.10"
	ipB2   = "198.51.100.20"
	ipHors = "192.0.2.77"
)

const secretTest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// faux noeud : verifie la signature comme le vrai et rend un port fixe.
type fauxNoeud struct {
	srv      *httptest.Server
	host     string
	port     int
	panne    atomic.Bool // /pair repond 503
	lent     time.Duration
	paires   atomic.Int64
	enCours  atomic.Int64
	maxCours atomic.Int64
	mu       sync.Mutex
	corps    []string
}

func nouveauFauxNoeud(t *testing.T, host string, port int) *fauxNoeud {
	t.Helper()
	f := &fauxNoeud{host: host, port: port}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m := hmac.New(sha256.New, []byte(secretTest))
		io.WriteString(m, r.Method+"\n"+r.URL.Path+"\n"+r.Header.Get("X-Relay-Time")+"\n")
		m.Write(body)
		sig, _ := hex.DecodeString(r.Header.Get("X-Relay-Sig"))
		if !hmac.Equal(sig, m.Sum(nil)) {
			http.Error(w, "signature", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/health":
			w.Write([]byte("ok"))
		case "/pair":
			n := f.enCours.Add(1)
			defer f.enCours.Add(-1)
			for {
				m := f.maxCours.Load()
				if n <= m || f.maxCours.CompareAndSwap(m, n) {
					break
				}
			}
			f.paires.Add(1)
			f.mu.Lock()
			f.corps = append(f.corps, string(body))
			f.mu.Unlock()
			if f.lent > 0 {
				time.Sleep(f.lent)
			}
			if f.panne.Load() {
				http.Error(w, "panne", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"host": f.host, "port": f.port})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func ecrireFichier(t *testing.T, dir, nom, contenu string) string {
	t.Helper()
	p := filepath.Join(dir, nom)
	if err := os.WriteFile(p, []byte(contenu), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// installerNoeuds charge le fichier de config comme en prod et remplace les noeuds regionaux
// le temps du test.
func installerNoeuds(t *testing.T, config string) []*RelayNode {
	t.Helper()
	nodes := LoadRelayNodes(config)
	regionalMu.Lock()
	avant := regionalNodes
	regionalNodes = nodes
	regionalMu.Unlock()
	t.Cleanup(func() {
		regionalMu.Lock()
		regionalNodes = avant
		regionalMu.Unlock()
	})
	limite := time.Now().Add(5 * time.Second)
	for _, n := range nodes {
		for !n.Healthy() {
			if time.Now().After(limite) {
				t.Fatalf("noeud %s jamais sain", n.URL)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nodes
}

// ancienneLecture est la lecture d origine (697417d), sans le controle de sante : la
// reference pour la retrocompatibilite d une ligne unique.
func ancienneLecture(path string) *RelayNode {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f := strings.Fields(string(raw))
	if len(f) < 4 || net.ParseIP(f[1]).To4() == nil {
		return nil
	}
	secret, err := os.ReadFile(f[2])
	if err != nil || len(bytes.TrimSpace(secret)) < 32 {
		return nil
	}
	ranges, err := loadRanges(f[3])
	if err != nil || len(ranges) == 0 {
		return nil
	}
	return &RelayNode{URL: strings.TrimRight(f[0], "/"), PublicIP: f[1], secret: bytes.TrimSpace(secret), ranges: ranges}
}

func TestRelayNodeLigneUniqueCommeAvant(t *testing.T) {
	dir := t.TempDir()
	cle := ecrireFichier(t, dir, "node.key", secretTest+"\n")
	court := ecrireFichier(t, dir, "court.key", "abc\n")
	plages := ecrireFichier(t, dir, "plages.txt", "# commentaire\n"+plageB+"\n"+plageA+"\n")
	vide := ecrireFichier(t, dir, "vide.txt", "# rien\n")
	cas := []string{
		"http://192.0.2.1:8790 192.0.2.1 " + cle + " " + plages,
		"http://192.0.2.1:8790 192.0.2.1 " + cle + " " + plages + "\n",
		"http://192.0.2.1:8790/ 192.0.2.1 " + cle + " " + plages + "\r\n",
		"  http://192.0.2.1:8790   192.0.2.1\t" + cle + " " + plages + " champ-en-trop\n",
		"http://192.0.2.1:8790 192.0.2.1 " + cle,
		"http://192.0.2.1:8790 pas-une-ip " + cle + " " + plages,
		"http://192.0.2.1:8790 2001:db8::1 " + cle + " " + plages,
		"http://192.0.2.1:8790 192.0.2.1 " + court + " " + plages,
		"http://192.0.2.1:8790 192.0.2.1 " + cle + " " + vide,
		"http://192.0.2.1:8790 192.0.2.1 " + filepath.Join(dir, "absent") + " " + plages,
		"",
	}
	for i, c := range cas {
		p := ecrireFichier(t, dir, "cfg"+strconv.Itoa(i)+".txt", c)
		ancien := ancienneLecture(p)
		nouveaux := readRelayNodes(p)
		if ancien == nil {
			if len(nouveaux) != 0 {
				t.Errorf("cas %d : l ancienne lecture refusait, la nouvelle rend %d noeud(s)", i, len(nouveaux))
			}
			continue
		}
		if len(nouveaux) != 1 {
			t.Fatalf("cas %d : %d noeuds, attendu 1", i, len(nouveaux))
		}
		n := nouveaux[0]
		if n.URL != ancien.URL || n.PublicIP != ancien.PublicIP || !bytes.Equal(n.secret, ancien.secret) || !reflect.DeepEqual(n.ranges, ancien.ranges) {
			t.Errorf("cas %d : lecture differente\n ancien %s %s %v\n nouveau %s %s %v", i, ancien.URL, ancien.PublicIP, ancien.ranges, n.URL, n.PublicIP, n.ranges)
		}
	}
	if readRelayNodes(filepath.Join(dir, "inexistant.txt")) != nil {
		t.Error("fichier absent : attendu nil")
	}
}

func TestRelayNodesPlusieursLignes(t *testing.T) {
	dir := t.TempDir()
	cle := ecrireFichier(t, dir, "node.key", secretTest)
	pa := ecrireFichier(t, dir, "a.txt", plageA+"\n")
	pb := ecrireFichier(t, dir, "b.txt", plageB+"\n")
	cfg := ecrireFichier(t, dir, "relay_node.txt", strings.Join([]string{
		"# noeuds regionaux",
		"http://192.0.2.1:8790 192.0.2.1 " + cle + " " + pa + "  # noeud regional",
		"",
		"ligne invalide",
		"http://192.0.2.2:8790/ 192.0.2.2 " + cle + " " + pb,
		"   # fin",
	}, "\n"))
	nodes := readRelayNodes(cfg)
	if len(nodes) != 2 || nodes[0].URL != "http://192.0.2.1:8790" || nodes[1].URL != "http://192.0.2.2:8790" {
		t.Fatalf("noeuds lus : %d %+v", len(nodes), nodes)
	}
	if nodes[0].PublicIP != "192.0.2.1" || nodes[1].PublicIP != "192.0.2.2" {
		t.Fatalf("ip publiques : %s %s", nodes[0].PublicIP, nodes[1].PublicIP)
	}
}

func configNoeud(t *testing.T, dir string, f *fauxNoeud, plage string) string {
	t.Helper()
	cle := ecrireFichier(t, dir, fmt.Sprintf("k%d.key", f.port), secretTest+"\n")
	pl := ecrireFichier(t, dir, fmt.Sprintf("p%d.txt", f.port), plage+"\n")
	return f.srv.URL + " " + f.host + " " + cle + " " + pl
}

// Une seule ligne : meme parcours qu avant (dans la zone -> noeud, sinon local ; cache).
func TestRegionalUneLigne(t *testing.T) {
	dir := t.TempDir()
	a := nouveauFauxNoeud(t, "192.0.2.1", 41001)
	installerNoeuds(t, ecrireFichier(t, dir, "relay_node.txt", configNoeud(t, dir, a, plageA)+"\n"))

	h, p, ok := regionalPairRelay(11, 22, ipA1, ipA2)
	if !ok || h != "192.0.2.1" || p != 41001 {
		t.Fatalf("paire dans la zone : %s:%d %v", h, p, ok)
	}
	if a.paires.Load() != 1 {
		t.Fatalf("/pair recus : %d", a.paires.Load())
	}
	a.mu.Lock()
	corps := a.corps[0]
	a.mu.Unlock()
	if corps != `{"ips":["203.0.113.10","203.0.113.20"],"pids":[11,22]}` {
		t.Fatalf("corps de /pair change : %s", corps)
	}
	// Cache : meme reponse, dans l autre ordre des PID, sans /pair synchrone.
	a.panne.Store(true)
	h, p, ok = regionalPairRelay(22, 11, ipA2, ipA1)
	if !ok || h != "192.0.2.1" || p != 41001 {
		t.Fatalf("paire en cache : %s:%d %v", h, p, ok)
	}
	a.panne.Store(false)

	avant := a.paires.Load()
	if _, _, ok := regionalPairRelay(33, 44, ipA1, ipHors); ok {
		t.Fatal("un joueur hors zone : attendu relais local")
	}
	if _, _, ok := regionalPairRelay(33, 44, ipHors, ipHors); ok {
		t.Fatal("deux joueurs hors zone : attendu relais local")
	}
	time.Sleep(50 * time.Millisecond)
	if n := a.paires.Load() - avant; n > 1 {
		// au plus le rearmement en arriere-plan de la paire en cache
		t.Fatalf("hors zone : %d /pair envoyes", n)
	}
	a.panne.Store(true)
	if _, _, ok := regionalPairRelay(55, 66, ipA1, ipA2); ok {
		t.Fatal("noeud qui refuse : attendu relais local")
	}
}

func TestRegionalDeuxNoeuds(t *testing.T) {
	dir := t.TempDir()
	a := nouveauFauxNoeud(t, "192.0.2.1", 41001)
	b := nouveauFauxNoeud(t, "192.0.2.2", 42002)
	installerNoeuds(t, ecrireFichier(t, dir, "relay_node.txt",
		configNoeud(t, dir, a, plageA)+"\n"+configNoeud(t, dir, b, plageB)+"\n"))

	if h, p, ok := regionalPairRelay(1, 2, ipA1, ipA2); !ok || h != "192.0.2.1" || p != 41001 {
		t.Fatalf("zone A : %s:%d %v", h, p, ok)
	}
	if h, p, ok := regionalPairRelay(3, 4, ipB1, ipB2); !ok || h != "192.0.2.2" || p != 42002 {
		t.Fatalf("zone B : %s:%d %v", h, p, ok)
	}
	if _, _, ok := regionalPairRelay(5, 6, ipA1, ipB1); ok {
		t.Fatal("un joueur dans chaque zone : attendu relais local")
	}
	if a.paires.Load() != 1 || b.paires.Load() != 1 {
		t.Fatalf("/pair recus : A=%d B=%d", a.paires.Load(), b.paires.Load())
	}
}

func TestRegionalPremierNoeudQuiSertLesDeux(t *testing.T) {
	dir := t.TempDir()
	a := nouveauFauxNoeud(t, "192.0.2.1", 41001)
	b := nouveauFauxNoeud(t, "192.0.2.2", 42002)
	// B sert A et B ; il est en premier : il prend aussi les paires de la zone A.
	installerNoeuds(t, ecrireFichier(t, dir, "relay_node.txt",
		configNoeud(t, dir, b, plageB+"\n"+plageA)+"\n"+configNoeud(t, dir, a, plageA)+"\n"))
	if h, _, ok := regionalPairRelay(1, 2, ipA1, ipA2); !ok || h != "192.0.2.2" {
		t.Fatalf("attendu le premier noeud de la liste, eu %s %v", h, ok)
	}
	if a.paires.Load() != 0 {
		t.Fatalf("le second noeud a ete sollicite : %d", a.paires.Load())
	}
}

func TestRegionalNoeudEnPanne(t *testing.T) {
	dir := t.TempDir()
	a := nouveauFauxNoeud(t, "192.0.2.1", 41001)
	b := nouveauFauxNoeud(t, "192.0.2.2", 42002)
	nodes := installerNoeuds(t, ecrireFichier(t, dir, "relay_node.txt",
		configNoeud(t, dir, a, plageA)+"\n"+configNoeud(t, dir, b, plageA)+"\n"))

	// A refuse : la paire passe au suivant.
	a.panne.Store(true)
	if h, p, ok := regionalPairRelay(1, 2, ipA1, ipA2); !ok || h != "192.0.2.2" || p != 42002 {
		t.Fatalf("A refuse : attendu B, eu %s:%d %v", h, p, ok)
	}
	// A declare malade par le controle de sante : on ne lui demande meme pas.
	a.panne.Store(false)
	nodes[0].healthy.Store(false)
	avant := a.paires.Load()
	if h, _, ok := regionalPairRelay(3, 4, ipA1, ipA2); !ok || h != "192.0.2.2" {
		t.Fatalf("A malade : attendu B, eu %s %v", h, ok)
	}
	if a.paires.Load() != avant {
		t.Fatal("un noeud malade a recu /pair")
	}
	// A injoignable (serveur arrete) mais encore cru sain : suivant.
	nodes[0].healthy.Store(true)
	a.srv.Close()
	if h, _, ok := regionalPairRelay(5, 6, ipA1, ipA2); !ok || h != "192.0.2.2" {
		t.Fatalf("A injoignable : attendu B, eu %s %v", h, ok)
	}
	// Tous refusent : relais local.
	b.panne.Store(true)
	if _, _, ok := regionalPairRelay(7, 8, ipA1, ipA2); ok {
		t.Fatal("tous les noeuds refusent : attendu relais local")
	}
	// Une paire en cache sur B reste servie par B.
	if h, _, ok := regionalPairRelay(2, 1, ipA2, ipA1); !ok || h != "192.0.2.2" {
		t.Fatalf("paire en cache sur B : %s %v", h, ok)
	}
}

// Les deux chemins (URL de session et initiation de sonde) arrivent souvent ensemble : un
// seul /pair, la meme reponse pour tous.
func TestRegionalArmementsSimultanes(t *testing.T) {
	dir := t.TempDir()
	a := nouveauFauxNoeud(t, "192.0.2.1", 41001)
	a.lent = 150 * time.Millisecond
	installerNoeuds(t, ecrireFichier(t, dir, "relay_node.txt", configNoeud(t, dir, a, plageA)))

	var wg sync.WaitGroup
	var mauvais atomic.Int64
	depart := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-depart
			pa, pb, ia, ib := uint64(7), uint64(9), ipA1, ipA2
			if i%2 == 1 {
				pa, pb, ia, ib = pb, pa, ib, ia
			}
			if h, p, ok := regionalPairRelay(pa, pb, ia, ib); !ok || h != "192.0.2.1" || p != 41001 {
				mauvais.Add(1)
			}
		}(i)
	}
	close(depart)
	wg.Wait()
	if mauvais.Load() != 0 {
		t.Fatalf("%d appels sans la reponse du noeud", mauvais.Load())
	}
	if a.maxCours.Load() != 1 || a.paires.Load() != 1 {
		t.Fatalf("/pair : %d en tout, %d simultanes, attendu 1 et 1", a.paires.Load(), a.maxCours.Load())
	}
}
