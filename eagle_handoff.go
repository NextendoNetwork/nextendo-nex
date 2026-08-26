package nex

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// Passation vers Eagle.
//
// Eagle est le relais qui porte reellement les parties a 99 (Tetris 99, SMB35,
// PAC-MAN 99, F-Zero 99). NEX ne fait que l'appariement : quand le salon est forme, le
// serveur previent chaque participant OU se trouve le relais et avec QUEL jeton s'y
// presenter. Sans cette notification le jeu finit son compte a rebours et s'arrete —
// il attend une adresse qui n'arrive jamais.
//
// Le porteur est un evenement de notification de REVISION 1. La revision 0, la seule
// que nous emettions jusqu'ici, n'a pas de m_mapParam — or c'est exactement la que
// voyagent l'url et le jeton. On ajoute donc la revision 1 A COTE de l'autre au lieu de
// la remplacer : notification.go porte l'avertissement que la Switch refuse la variante
// a MapParam, ce qui est vrai pour les titres qui attendent la revision 0.
const (
	// EagleNotificationType : le type d'evenement que libeagle guette.
	EagleNotificationType uint32 = 200000
	// EagleNotificationSource : Quazal Rendez-Vous, la source que le vrai service annonce.
	EagleNotificationSource uint64 = 257049437023956657
)

// notifParams32 : ecrire les parametres de l'evenement en 32 bits au lieu de 64.
var notifParams32 = os.Getenv("NEX_NOTIF_PARAMS32") == "1"

// NotificationEventV1 est l'evenement de notification Switch en REVISION 1 : les memes
// champs que la revision 0, plus la carte de parametres.
type NotificationEventV1 struct {
	PIDSource uint64
	Type      uint32
	Param1    uint64
	Param2    uint64
	StrParam  string
	Param3    uint64
	MapParam  map[string]Variant
}

// Levels implements Structure.
func (n *NotificationEventV1) Levels() []Level {
	return []Level{{
		Version: 1,
		Save: func(o *StreamOut) {
			o.PID(n.PIDSource)
			o.U32(n.Type)
			// Largeur des parametres. La doc de kinnay donne des Uint64 pour la Switch et
			// c'est ce que les autres titres acceptent — mais PAC-MAN 99 ignore l'evenement
			// en silence, et notification.go garde trace d'essais en Uint32. Reglable par
			// NEX_NOTIF_PARAMS32=1 pour trancher par l'experience plutot que par l'exegese.
			if notifParams32 {
				o.U32(uint32(n.Param1))
				o.U32(uint32(n.Param2))
				o.String(n.StrParam)
				o.U32(uint32(n.Param3))
			} else {
				o.U64(n.Param1)
				o.U64(n.Param2)
				o.String(n.StrParam)
				o.U64(n.Param3)
			}
			o.U32(uint32(len(n.MapParam)))
			for k, v := range n.MapParam {
				o.String(k)
				o.Variant(v)
			}
		},
		Load: func(i *StreamIn) {
			n.PIDSource = i.PID()
			n.Type = i.U32()
			n.Param1 = i.U64()
			n.Param2 = i.U64()
			n.StrParam = i.String()
			n.Param3 = i.U64()
			n.MapParam = map[string]Variant{}
			for c := i.U32(); c > 0; c-- {
				k := i.String()
				n.MapParam[k] = i.Variant()
			}
		},
	}}
}

// eagleTokenPayload : ce que le jeton porte, avant encodage.
type eagleTokenPayload struct {
	ExpiresAt string `json:"expires_at"` // horodatage en SECONDES, en chaine
	ServerEnv string `json:"server_env"` // "lp1"
	ServerID  string `json:"server_id"`  // 20 caracteres alphanumeriques minuscules
	UserID    string `json:"user_id"`    // le PID du joueur, en hexadecimal
}

// eagleToken : l'enveloppe signee.
type eagleToken struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
	Version   int    `json:"version"`
}

// BuildEagleToken fabrique le jeton qu'un joueur presentera au relais.
//
// C'est NOTRE serveur Eagle qui le verifiera, donc la signature n'a a convaincre que
// nous-memes : un HMAC-SHA256 sur la charge utile encodee suffit. Le client, lui, ne
// fait que transporter la chaine sans la lire.
func BuildEagleToken(secret []byte, pid uint64, serverID string, duree time.Duration) (string, error) {
	brut, err := json.Marshal(eagleTokenPayload{
		ExpiresAt: strconv.FormatInt(time.Now().Add(duree).Unix(), 10),
		ServerEnv: "lp1",
		ServerID:  serverID,
		UserID:    fmt.Sprintf("%x", pid),
	})
	if err != nil {
		return "", err
	}
	charge := base64.StdEncoding.EncodeToString(brut)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(charge))

	enveloppe, err := json.Marshal(eagleToken{
		Payload:   charge,
		Signature: base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		Version:   1,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enveloppe), nil
}

// SendEagleHandoff previent un participant de l'adresse du relais et lui remet son jeton.
// Le jeton est PROPRE A CHAQUE joueur : il porte son PID, donc il se fabrique par
// destinataire et ne peut pas etre diffuse tel quel a tout le salon.
func SendEagleHandoff(target *Connection, gid uint32, url, token string) {
	s := target.Settings
	out := NewStreamOut(s)
	out.Add(&NotificationEventV1{
		PIDSource: EagleNotificationSource,
		Type:      EagleNotificationType,
		Param1:    uint64(gid),
		MapParam: map[string]Variant{
			"url":   {Type: VariantString, String: url},
			"token": {Type: VariantString, String: token},
		},
	})
	callID := 0xFFFD0000 + atomicNextEagleCall()
	target.SendRMC(NewRMCRequest(s, ProtocolNotifications, MethodProcessNotificationEvent, callID, out.Bytes()))
	fmt.Printf("[Eagle] passation -> pid=%d gid=%d url=%s (jeton %d o)\n", target.PID, gid, url, len(token))
}

var eagleCallCounter uint32

func atomicNextEagleCall() uint32 { return atomic.AddUint32(&eagleCallCounter, 1) }

// SendNotificationTest envoie un evenement de notification ORDINAIRE (revision 0) d'un
// type choisi, pour repondre a une question qu'on n'a jamais posee : ce jeu traite-t-il
// SEULEMENT nos notifications ?
//
// Tout ce qui marche aujourd'hui dans PAC-MAN 99 part du CLIENT — il interroge les
// participants, il envoie ses messages. Rien ne prouve qu'il ecoute ce que NOUS poussons.
// Si le canal etait mort, la passation vers Eagle echouerait exactement comme on
// l'observe : envoyee, jamais honoree, sans erreur.
//
// Le type 109000 (salon supprime) est choisi parce que sa reaction est INRATABLE : le jeu
// doit quitter le salon. Pas de reaction = pas d'ecoute.
func SendNotificationTest(target *Connection, typ uint32, param1 uint64) {
	s := target.Settings
	out := NewStreamOut(s)
	out.Add(&NotificationEvent{
		PIDSource: target.PID,
		Type:      typ,
		Param1:    param1,
	})
	callID := 0xFFFC0000 + atomicNextEagleCall()
	target.SendRMC(NewRMCRequest(s, ProtocolNotifications, MethodProcessNotificationEvent, callID, out.Bytes()))
	fmt.Printf("[Notif TEST] type=%d param1=%d -> pid=%d\n", typ, param1, target.PID)
}
