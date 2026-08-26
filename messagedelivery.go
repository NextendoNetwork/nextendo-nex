package nex

import (
	"fmt"
	"os"
)

// Message Delivery (protocole 27). UNE seule methode : DeliverMessage.
//
// Le serveur ne REPOND pas a cet appel — la documentation de kinnay est explicite
// (« No RMC response is sent »). Il RELAIE : il repousse la meme requete vers les autres
// membres du salon. Repondre au lieu de relayer laisse l'expediteur satisfait et tous les
// autres dans le noir.
//
// PAC-MAN 99 s'en sert pour diffuser l'etat du salon : la charge utile capturee porte un
// « BinaryMessage » contenant le champ « participants_cnt ». C'est ce compteur que le jeu
// affiche pendant « Recherche de partie » ; sans relais il reste a zero pour tout le monde.
const (
	ProtocolMessageDelivery uint16 = 0x1B
	MethodDeliverMessage    uint32 = 0x01
)

// relaiInclutSource : renvoyer aussi le message A SON EXPEDITEUR.
//
// On l'excluait, en se disant qu'il connait deja ce qu'il vient d'ecrire. MESURE DU
// 2026-08-26 : dans PAC-MAN 99 le CREATEUR du salon reste bloque a 0 pendant que les
// autres voient un compte — asymetrie exactement compatible avec un jeu qui affiche non
// pas ce qu'il a calcule, mais ce que le SERVEUR lui renvoie. Exclu de sa propre
// diffusion, l'hote n'apprend donc jamais rien.
//
// Le protocole Eagle connait les deux modes (« tous sauf la source » et « tous, source
// comprise »), donc renvoyer a l'expediteur n'est pas un bricolage.
// Pilote par NEX_RELAI_INCLUT_SOURCE=1.
var relaiInclutSource = os.Getenv("NEX_RELAI_INCLUT_SOURCE") == "1"

// MessageDeliveryHandler relaie DeliverMessage aux autres participants du salon de
// l'appelant. Le corps est retransmis TEL QUEL : le serveur n'a pas a comprendre le
// message, seulement a savoir a qui il s'adresse — c'est un relais, comme Eagle.
func (m *Matchmaking) MessageDeliveryHandler() RMCHandler {
	return func(conn *Connection, req *RMCMessage) *RMCMessage {
		// Le salon de l'appelant. ATTENTION : un joueur figure souvent dans PLUSIEURS
		// salons — chaque reconnexion en cree un nouveau et l'ancien n'est vide qu'a la
		// deconnexion propre (RemovePlayer). Prendre « le premier trouve » revient a tirer
		// au sort, l'iteration d'une map Go etant volontairement desordonnee : on tombait
		// presque toujours sur un salon perime a un seul membre et le message partait a la
		// poubelle. Mesure du 2026-08-26 : neuf salons pour deux joueurs, un seul relais
		// effectif sur dix.
		//
		// On retient donc le salon le PLUS PEUPLE parmi ceux qui contiennent l'appelant, et
		// a egalite le plus recent (gid le plus grand). Relayer dans un salon a un membre
		// n'a de toute facon aucun effet, donc preferer le peuple est sans risque.
		m.mu.Lock()
		var destinataires []uint64
		meilleurGID := uint32(0)
		for gid, g := range m.gatherings {
			membre := false
			for _, p := range g.participants {
				if p == conn.PID {
					membre = true
					break
				}
			}
			if !membre {
				continue
			}
			if len(g.participants) > len(destinataires) ||
				(len(g.participants) == len(destinataires) && gid > meilleurGID) {
				destinataires = append(destinataires[:0], g.participants...)
				meilleurGID = gid
			}
		}
		m.mu.Unlock()

		relayes := 0
		for _, pid := range destinataires {
			if pid == conn.PID && !relaiInclutSource {
				continue
			}
			c := conn.Endpoint.FindConnectionByPID(pid)
			if c == nil {
				continue // participant enregistre mais deconnecte : rien a relayer
			}
			c.SendRMC(NewRMCRequest(c.Settings, ProtocolMessageDelivery, MethodDeliverMessage, req.CallID, req.Body))
			relayes++
		}
		fmt.Printf("[MsgDelivery] de pid=%d : %d o relayes vers %d destinataire(s) (salon gid=%d, %d membre(s))\n",
			conn.PID, len(req.Body), relayes, meilleurGID, len(destinataires))

		return nil // aucune reponse RMC : c'est le protocole, pas un oubli
	}
}
