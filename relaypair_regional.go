package nex

// Regional pair relay. When both players of a relayed pair are in the regional node's area (a node serving
// given address ranges), the pair's port is opened on that node instead of here, so two
// nearby players no longer go through a distant relay. Same relay rules on the node (see
// the relay node service). Anything else — one player outside the area, node down, node refusing —
// falls through to the local relay exactly as before.
//
// Config file: NEXTENDO_RELAY_NODE_FILE (default relay_node.txt), see relaynode.go. Absent = off.
//
// Plusieurs noeuds (une ligne chacun) : la paire va au PREMIER noeud sain qui sert les DEUX
// adresses. S il refuse ou ne repond pas, on essaie le suivant qui les sert aussi, puis le
// relais local, exactement comme avec un seul noeud. Chaque noeud garde son propre cache de
// paires : une paire ouverte sur un noeud tombe ne bloque pas les autres.

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	regionalMu    sync.Mutex
	regionalNodes []*RelayNode
	regionalTried bool
)

type regionalPair struct {
	host    string
	port    int
	expires time.Time

	// Dernier /pair envoye pour la paire (corps et seconde). Un renvoi au corps identique
	// dans la meme seconde serait octet pour octet la meme requete signee : il n apporte
	// rien et un noeud qui refuse les rejeux le rejetterait.
	body   string
	second int64
}

// regionalArm est un armement en cours, partage par les appels simultanes de la meme paire.
type regionalArm struct {
	done chan struct{}
	host string
	port int
	err  error
}

// loadRegionalNode reads the node config once, when the pair relay is first armed.
func loadRegionalNode() {
	regionalMu.Lock()
	defer regionalMu.Unlock()
	if regionalTried {
		return
	}
	regionalTried = true
	path := os.Getenv("NEXTENDO_RELAY_NODE_FILE")
	if path == "" {
		path = "relay_node.txt"
	}
	regionalNodes = LoadRelayNodes(path)
	for _, n := range regionalNodes {
		fmt.Printf("[relais-paire] regional node %s (%s) for pairs inside its area\n", n.URL, n.PublicIP)
	}
}

// regionalPairRelay ouvre la paire sur le premier noeud sain qui sert les deux joueurs ;
// faux si aucun ne l a acceptee (l appelant prend alors le relais local).
func regionalPairRelay(pidA, pidB uint64, ipA, ipB string) (string, int, bool) {
	regionalMu.Lock()
	nodes := regionalNodes
	regionalMu.Unlock()
	for _, node := range nodes {
		if !node.Serves(ipA) || !node.Serves(ipB) {
			continue
		}
		host, port, err := node.pairFor(pidA, pidB, ipA, ipB)
		if err != nil {
			fmt.Printf("[relais-paire] regional node %s refused pid=%d/%d (%v) : noeud suivant ou relais local\n", node.URL, pidA, pidB, err)
			continue
		}
		return host, port, true
	}
	return "", 0, false
}

// pairFor rend l adresse de la paire sur ce noeud : depuis le cache (avec un rearmement en
// arriere-plan, comme avant), sinon par un /pair synchrone.
func (n *RelayNode) pairFor(pidA, pidB uint64, ipA, ipB string) (string, int, error) {
	lo, hi := pidA, pidB
	if lo > hi {
		lo, hi = hi, lo
	}
	key := [2]uint64{lo, hi}
	body := regionalBody(pidA, pidB, ipA, ipB)
	now := time.Now()

	n.pairsMu.Lock()
	if n.pairs == nil {
		n.pairs = map[[2]uint64]regionalPair{}
		n.inflight = map[[2]uint64]*regionalArm{}
	}
	if p, ok := n.pairs[key]; ok && now.Before(p.expires) {
		rearm := p.body != string(body) || p.second != now.Unix()
		if rearm {
			p.body, p.second = string(body), now.Unix()
			n.pairs[key] = p
		}
		n.pairsMu.Unlock()
		// Re-arm on the node so it refreshes the expected addresses (cheap, keeps both paths in sync).
		if rearm {
			go n.armRegional(body)
		}
		return p.host, p.port, nil
	}
	if c, ok := n.inflight[key]; ok {
		n.pairsMu.Unlock()
		<-c.done
		return c.host, c.port, c.err
	}
	c := &regionalArm{done: make(chan struct{})}
	n.inflight[key] = c
	n.pairsMu.Unlock()

	c.host, c.port, c.err = n.armRegional(body)

	n.pairsMu.Lock()
	delete(n.inflight, key)
	if c.err == nil {
		n.pairs[key] = regionalPair{host: c.host, port: c.port, expires: time.Now().Add(90 * time.Second),
			body: string(body), second: now.Unix()}
		for k, p := range n.pairs {
			if time.Now().After(p.expires) {
				delete(n.pairs, k)
			}
		}
	}
	n.pairsMu.Unlock()
	close(c.done)
	if c.err == nil {
		fmt.Printf("[relais-paire] pid=%d <-> pid=%d (%s, %s) -> regional %s:%d\n", pidA, pidB, ipA, ipB, c.host, c.port)
	}
	return c.host, c.port, c.err
}

// regionalBody est le corps de /pair, dans l ordre des PID donne par l appelant (le noeud
// range lui-meme les adresses attendues).
func regionalBody(pidA, pidB uint64, ipA, ipB string) []byte {
	body, _ := json.Marshal(map[string]any{"pids": [2]uint64{pidA, pidB}, "ips": [2]string{ipA, ipB}})
	return body
}

func (n *RelayNode) armRegional(body []byte) (string, int, error) {
	out, err := n.Do("POST", "/pair", body)
	if err != nil {
		return "", 0, err
	}
	var resp struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Host == "" || resp.Port <= 0 {
		return "", 0, fmt.Errorf("bad reply %q", out)
	}
	return resp.Host, resp.Port, nil
}
