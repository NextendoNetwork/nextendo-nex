package nex

import (
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
//	[0:4]    52 46 00 00   signature
//	[4:20]   identifiant Mii
//	[20:42]  nom Mii, UTF-16LE, 22 octets
//	[42:...] pseudo en jeu, UTF-16LE
//
// Tout ce qui sort d'ici vient du CLIENT. C'est une donnée hostile, pas une
// identité : bornée, nettoyée, et jamais suffisante à elle seule pour sanctionner.

const (
	// commonDataMiiNameOff / Len : le nom Mii, à position fixe dans le bloc.
	commonDataMiiNameOff = 20
	commonDataMiiNameLen = 22
	// commonDataNickOff : le pseudo en jeu suit immédiatement le nom Mii. Sa
	// longueur n'est pas documentée par la capture, on lit donc jusqu'au
	// terminateur plutôt que de figer une taille qu'on ne connaît pas.
	commonDataNickOff = commonDataMiiNameOff + commonDataMiiNameLen
	// commonDataMaxNameRunes borne ce qui ressort. Le client peut déposer ce
	// qu'il veut ; un nom démesuré dans un journal ou une interface de
	// modération serait un moyen de nuire, pas un nom.
	commonDataMaxNameRunes = 32
)

// commonDataMagic : les quatre premiers octets du bloc. Un profil qui ne les
// porte pas n'a pas été produit par un jeu intact — c'est en soi un signal.
var commonDataMagic = []byte{0x52, 0x46, 0x00, 0x00}

// CommonDataNames rend le nom Mii et le pseudo en jeu déposés dans ce profil.
//
// ok vaut false quand le bloc ne correspond pas à la disposition connue : trop
// court, ou signature absente. Ce n'est pas une erreur à ignorer — un profil
// non conforme déposé par une connexion authentifiée mérite d'être remonté.
func CommonDataNames(blob []byte) (miiName, nickname string, ok bool) {
	if len(blob) < commonDataNickOff+2 {
		return "", "", false
	}
	for i, b := range commonDataMagic {
		if blob[i] != b {
			return "", "", false
		}
	}
	miiName = decodeUTF16LE(blob[commonDataMiiNameOff : commonDataMiiNameOff+commonDataMiiNameLen])
	nickname = decodeUTF16LE(blob[commonDataNickOff:])
	return miiName, nickname, true
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
