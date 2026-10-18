package nex

// Client side of the relay node service, copied into each game server (package name adjusted there).
// Configured by one file, read at startup:
//   <url> <public ip> <secret file> <americas ranges file>
// e.g. "http://198.51.100.50:8790 198.51.100.50 /chemin/relay_node.key /chemin/relay_americas.txt".
// Missing or unreadable file = no regional relay: every caller keeps using its own (prod) relay.
//
// Plusieurs noeuds : une ligne par noeud, dans l ordre de preference, "#" ouvre un
// commentaire jusqu a la fin de la ligne. La ligne unique d origine reste valable telle
// quelle. Une ligne invalide est ignoree (journalisee) sans faire tomber les autres.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RelayNode is a regional relay reachable over its signed control API.
type RelayNode struct {
	URL, PublicIP string
	secret        []byte
	ranges        [][2]uint32
	healthy       atomic.Bool
	http          *http.Client

	// Cache des paires ouvertes sur CE noeud (relaypair_regional.go), et armements en
	// cours : deux appels simultanes pour la meme paire attendent le meme /pair au lieu
	// d en envoyer deux.
	pairsMu  sync.Mutex
	pairs    map[[2]uint64]regionalPair
	inflight map[[2]uint64]*regionalArm
}

// LoadRelayNode reads the config file; nil when absent or invalid. Avec plusieurs lignes, rend
// le premier noeud valide (compatibilite : les appelants d une seule ligne).
func LoadRelayNode(path string) *RelayNode {
	if nodes := LoadRelayNodes(path); len(nodes) > 0 {
		return nodes[0]
	}
	return nil
}

// LoadRelayNodes lit tous les noeuds du fichier, dans l ordre des lignes, et lance leur
// controle de sante. Fichier absent ou sans ligne valide = nil.
func LoadRelayNodes(path string) []*RelayNode {
	nodes := readRelayNodes(path)
	for _, n := range nodes {
		go n.healthLoop()
	}
	return nodes
}

// readRelayNodes lit le fichier sans rien lancer.
func readRelayNodes(path string) []*RelayNode {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var nodes []*RelayNode
	for i, line := range strings.Split(string(raw), "\n") {
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = line[:j]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		n, err := parseRelayNode(f)
		if err != nil {
			fmt.Printf("[relay-node] %s ligne %d ignoree : %v\n", path, i+1, err)
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// parseRelayNode lit les champs d une ligne : <url> <ip publique> <fichier secret> <fichier plages>.
// Memes exigences que la lecture d origine (IPv4, secret d au moins 32 octets, plages non vides).
func parseRelayNode(f []string) (*RelayNode, error) {
	if len(f) < 4 || net.ParseIP(f[1]).To4() == nil {
		return nil, fmt.Errorf("attendu <url> <ip publique> <fichier secret> <fichier plages>")
	}
	secret, err := os.ReadFile(f[2])
	if err != nil || len(bytes.TrimSpace(secret)) < 32 {
		return nil, fmt.Errorf("secret %s illisible ou trop court", f[2])
	}
	ranges, err := loadRanges(f[3])
	if err != nil || len(ranges) == 0 {
		return nil, fmt.Errorf("plages %s illisibles ou vides (%v)", f[3], err)
	}
	return &RelayNode{URL: strings.TrimRight(f[0], "/"), PublicIP: f[1], secret: bytes.TrimSpace(secret), ranges: ranges,
		http: &http.Client{Timeout: 2 * time.Second}}, nil
}

func loadRanges(path string) ([][2]uint32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][2]uint32
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		a, b, ok := strings.Cut(line, "-")
		lo, hi := ip4(a), ip4(b)
		if !ok || lo == 0 || hi < lo {
			return nil, fmt.Errorf("bad range %q", line)
		}
		out = append(out, [2]uint32{lo, hi})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

func ip4(s string) uint32 {
	ip := net.ParseIP(strings.TrimSpace(s)).To4()
	if ip == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip)
}

// Serves reports whether ip belongs to this node's region (and the node is up).
func (n *RelayNode) Serves(ip string) bool {
	if n == nil || !n.healthy.Load() {
		return false
	}
	x := ip4(ip)
	if x == 0 {
		return false
	}
	i := sort.Search(len(n.ranges), func(i int) bool { return n.ranges[i][1] >= x })
	return i < len(n.ranges) && n.ranges[i][0] <= x
}

// Healthy reports the last health check.
func (n *RelayNode) Healthy() bool { return n != nil && n.healthy.Load() }

func (n *RelayNode) healthLoop() {
	for {
		_, err := n.Do("GET", "/health", nil)
		if was := n.healthy.Swap(err == nil); was != (err == nil) {
			fmt.Printf("[relay-node] %s healthy=%v (%v)\n", n.URL, err == nil, err)
		}
		time.Sleep(10 * time.Second)
	}
}

// Do sends a signed request and returns the body of a 200 reply.
func (n *RelayNode) Do(method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, n.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	m := hmac.New(sha256.New, n.secret)
	io.WriteString(m, method+"\n"+path+"\n"+ts+"\n")
	m.Write(body)
	req.Header.Set("X-Relay-Time", ts)
	req.Header.Set("X-Relay-Sig", hex.EncodeToString(m.Sum(nil)))
	resp, err := n.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return out, nil
}
