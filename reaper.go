package nex

import (
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// Éviction des connexions mortes.
//
// Le protocole PRUDP n'a pas de fermeture fiable : une console qui crashe, perd le réseau
// ou dont l'émulateur est fermé ne dit jamais au revoir. Sans éviction, sa connexion restait
// enregistrée POUR TOUJOURS. Conséquences mesurées en production le 20/07/2026 :
//   - des connexions « actives » depuis 24 h, 45 h, 47 h sans un seul paquet ;
//   - sur Mario Kart, la TOTALITÉ des 9 joueurs listés étaient des connexions mortes ;
//   - la garde « un seul endroit à la fois » y voyait le joueur et lui refusait l'accès :
//     des membres se sont retrouvés définitivement dehors et ont dû se recréer un compte ;
//   - les compteurs de joueurs connectés étaient faux (Smash affichait 14 pour 7 réels).
//
// On évince donc toute connexion sans trafic depuis reaperMaxIdle. Le seuil est large :
// une partie génère du trafic en continu, et une console en pause dans un menu envoie
// encore des PING. Réglable par NEXTENDO_REAP_IDLE_SECONDS (0 = éviction désactivée).

const (
	defaultReapIdleSeconds = 600 // 10 min sans le moindre paquet = connexion morte
	defaultReapEverySecond = 60  // fréquence de passage du reaper
)

func envSeconds(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(def) * time.Second
}

// ReapIdleTimeout : durée sans trafic au-delà de laquelle une connexion est évincée.
func ReapIdleTimeout() time.Duration {
	return envSeconds("NEXTENDO_REAP_IDLE_SECONDS", defaultReapIdleSeconds)
}

// snapshotConns renvoie une copie de la liste des connexions. On ne fait JAMAIS de close()
// en tenant connMu : close() appelle unregisterConnection, qui reprend ce même verrou.
func (e *Endpoint) snapshotConns() []*Connection {
	e.connMu.Lock()
	out := make([]*Connection, 0, len(e.connections))
	for _, c := range e.connections {
		out = append(out, c)
	}
	e.connMu.Unlock()
	return out
}

// ReapIdle ferme les connexions sans trafic depuis maxIdle et renvoie le nombre évincé.
// maxIdle <= 0 désactive l'éviction (aucune connexion n'est touchée).
func (e *Endpoint) ReapIdle(maxIdle time.Duration) int {
	if maxIdle <= 0 {
		return 0
	}
	n := 0
	for _, c := range e.snapshotConns() {
		// lastSeen à zéro = poignée de main en cours, jamais évincée.
		if idle := c.IdleFor(); idle > 0 && idle >= maxIdle {
			log.Printf("[reaper] connexion morte évincée : pid=%d rvcid=%d addr=%s (sans trafic depuis %s)",
				c.PID, c.ID, c.RemoteAddr, idle.Round(time.Second))
			c.Close()
			n++
		}
	}
	return n
}

// KickPID ferme toutes les connexions d'un compte et renvoie le nombre fermé. Sert à
// libérer manuellement un joueur resté coincé (« ce compte joue déjà ailleurs ») sans
// redémarrer le serveur, ce qui déconnecterait tout le monde.
func (e *Endpoint) KickPID(pid uint64) int {
	if pid == 0 {
		return 0
	}
	n := 0
	for _, c := range e.snapshotConns() {
		if c.PID == pid {
			log.Printf("[kick] pid=%d rvcid=%d addr=%s déconnecté (demande administrateur)", c.PID, c.ID, c.RemoteAddr)
			c.Close()
			n++
		}
	}
	return n
}

// KickConnection ferme UNE connexion par son rvcid. true si elle existait.
func (e *Endpoint) KickConnection(id uint32) bool {
	for _, c := range e.snapshotConns() {
		if c.ID == id {
			log.Printf("[kick] rvcid=%d pid=%d addr=%s déconnecté (demande administrateur)", c.ID, c.PID, c.RemoteAddr)
			c.Close()
			return true
		}
	}
	return false
}

var reaperOnce sync.Once

// StartReaper lance l'éviction périodique en tâche de fond. Idempotent : appelable depuis
// chaque serveur de jeu sans risque de démarrer plusieurs boucles.
func (e *Endpoint) StartReaper() {
	maxIdle := ReapIdleTimeout()
	if maxIdle <= 0 {
		log.Printf("[reaper] éviction DÉSACTIVÉE (NEXTENDO_REAP_IDLE_SECONDS=0)")
		return
	}
	every := envSeconds("NEXTENDO_REAP_EVERY_SECONDS", defaultReapEverySecond)
	reaperOnce.Do(func() {
		log.Printf("[reaper] actif : éviction des connexions sans trafic depuis %s (passage toutes les %s)", maxIdle, every)
		go func() {
			for {
				time.Sleep(every)
				if n := e.ReapIdle(maxIdle); n > 0 {
					log.Printf("[reaper] %d connexion(s) morte(s) évincée(s)", n)
				}
			}
		}()
	})
}
