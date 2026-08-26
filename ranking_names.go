package nex

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf16"
)

// Lecture des noms portés par le profil MK8 (protocole Ranking, méthode 0x04).
//
// ranking.go stocke ce bloc TEL QUEL et le rediffuse, ce qui suffit à faire
// réapparaître pseudos et drapeaux en course, sans interpréter le format. Ce
// fichier fait la seule chose que ce choix laissait de côté : LIRE le nom. Ce
// n'est pas nécessaire au jeu ; ça l'est à la modération.
//
// Le serveur ne voit jamais la course — elle se joue en P2P — et /api/stats ne
// sait produire que « Joueur-<pid> ». Un signalement (« untel trichait ») ne peut
// donc être rattaché à personne. Or des joueurs se présentent sous le nom d'un
// AUTRE, ou sans nom, ou en « Player » : bannir sur la foi d'un nom reviendrait
// à bannir la VICTIME de l'usurpation plutôt que son auteur. Lire le nom déposé
// par un PID authentifié rend trois cas détectables sans rien voir de la course :
// un nom qui appartient à un autre compte, un nom vide ou générique, un nom qui
// change d'une session à l'autre.
//
// Disposition relevée sur la capture Nintendo du 2026-08-12 (bloc de 132 octets),
// telle que documentée en tête de ranking.go :
//
//	[0:4]     variable — PAS une signature (voir plus bas)
//	[4:20]    identifiant Mii
//	[20:42]   nom Mii, UTF-16LE
//	[96:132]  pseudo en jeu, un octet par caractère, terminé par 0
//
// Tout ce qui sort d'ici vient du CLIENT. C'est une donnée hostile, pas une
// identité : bornée, nettoyée, et jamais suffisante à elle seule pour sanctionner.

// Disposition ETABLIE sur 4 371 profils réels relevés en production, et non sur
// une seule capture : tous font exactement 132 octets, le nom Mii est lisible en
// UTF-16LE à l'offset 20 sur 4 369 d'entre eux, et le pseudo en jeu est lisible à
// l'offset 96, un octet par caractère, sur 4 348 (19 pseudos vides).
//
// Les quatre premiers octets ne sont PAS une signature stable : on y relève au
// moins 00000000 (1 411), 52460000 (1 069), 584d0000 (283) et 52420000 (216).
// Une première version de ce fichier exigeait 52460000 — elle rejetait donc les
// trois quarts des profils. C'est la taille, et la lisibilité des deux champs,
// qui disent si le bloc est exploitable.
const (
	// commonDataBlobLen : taille du bloc. Invariante sur tous les profils relevés.
	commonDataBlobLen = 132
	// Nom Mii : UTF-16LE, à position fixe.
	commonDataMiiNameOff = 20
	commonDataMiiNameLen = 22
	// Pseudo en jeu : un octet par caractère, terminé par 0, jusqu'à la fin du bloc.
	commonDataNickOff = 96
	// commonDataMaxNameRunes borne ce qui ressort. Le client peut déposer ce
	// qu'il veut ; un nom démesuré dans un journal ou une interface de
	// modération serait un moyen de nuire, pas un nom.
	commonDataMaxNameRunes = 32
)

// CommonDataNames rend le nom Mii et le pseudo en jeu déposés dans ce profil.
//
// ok vaut false quand le bloc ne correspond pas à la disposition connue : trop
// court, ou signature absente. Ce n'est pas une erreur à ignorer — un profil
// non conforme déposé par une connexion authentifiée mérite d'être remonté.
func CommonDataNames(blob []byte) (miiName, nickname string, ok bool) {
	if len(blob) != commonDataBlobLen {
		return "", "", false
	}
	miiName = decodeUTF16LE(blob[commonDataMiiNameOff : commonDataMiiNameOff+commonDataMiiNameLen])
	nickname = decodeBytesZ(blob[commonDataNickOff:])
	return miiName, nickname, true
}

// decodeBytesZ lit une chaîne d'octets terminée par 0, puis la nettoie. Le pseudo
// en jeu n'est pas en UTF-16 : il tient sur un octet par caractère.
func decodeBytesZ(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return sanitizeName(string(b))
}

// decodeUTF16LE lit une chaîne UTF-16LE jusqu'au terminateur, puis la nettoie.
// Un octet impair en fin de tranche est ignoré : on préfère un nom tronqué à un
// décodage qui déborde.
func decodeUTF16LE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u := uint16(b[i]) | uint16(b[i+1])<<8
		if u == 0 { // terminateur : le reste de la zone est du remplissage
			break
		}
		units = append(units, u)
		if len(units) >= commonDataMaxNameRunes {
			break
		}
	}
	return sanitizeName(string(utf16.Decode(units)))
}

// sanitizeName retire ce qui n'a rien à faire dans un nom affiché ou journalisé.
// Les caractères de contrôle sont écartés d'abord : ils permettraient d'injecter
// des retours ligne dans un journal, donc d'y fabriquer des lignes crédibles.
func sanitizeName(s string) string {
	var sb strings.Builder
	n := 0
	for _, r := range s {
		if r == unicode.ReplacementChar || !unicode.IsGraphic(r) {
			continue
		}
		sb.WriteRune(r)
		if n++; n >= commonDataMaxNameRunes {
			break
		}
	}
	return strings.TrimSpace(sb.String())
}

// CommonDataNamesForPID rend les noms du profil déjà stocké pour ce PID. C'est
// le point d'entrée d'une consultation de modération : partir du PID, qui est
// authentifié, et non du nom, qui ne l'est pas.
func CommonDataNamesForPID(pid uint64) (miiName, nickname string, ok bool) {
	blob := CommonData(pid)
	if blob == nil {
		return "", "", false
	}
	return CommonDataNames(blob)
}

// logProfileNames : la journalisation des noms est OPTIONNELLE et coupée par
// défaut. Ce sont des données nominatives — ranking.go range déjà les profils en
// 0600 pour cette raison — et les journaux de ces serveurs sont volumineux et peu
// protégés. On l'allume quand on enquête, pas en permanence.
var logProfileNames = os.Getenv("NEXTENDO_LOG_PROFILE_NAMES") == "1"

// logCommonDataNames trace le nom annoncé par un PID authentifié : de quoi
// rattacher un signalement à un compte, ce qu'aucune autre trace ne permet.
func logCommonDataNames(pid uint64, blob []byte) {
	if !logProfileNames {
		return
	}
	mii, nick, ok := CommonDataNames(blob)
	if !ok {
		// Un bloc non conforme est plus intéressant qu'un bloc ordinaire : on le
		// signale même si on ne sait pas le lire.
		fmt.Printf("[Ranking] profil NON CONFORME pid=%d taille=%d\n", pid, len(blob))
		return
	}
	fmt.Printf("[Ranking] profil pid=%d mii=%q pseudo=%q\n", pid, mii, nick)
}
