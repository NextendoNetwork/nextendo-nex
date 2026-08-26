package nex

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// profilMK8 fabrique un bloc conforme à la disposition relevée sur la capture
// Nintendo, pour éprouver le décodage sans dépendre d'un profil réel — les vrais
// sont des données nominatives et n'ont rien à faire dans un dépôt.
func profilMK8(mii, pseudo string) []byte {
	b := make([]byte, commonDataBlobLen)
	// Nom Mii : UTF-16LE a l'offset 20.
	for i, u := range utf16.Encode([]rune(mii)) {
		off := commonDataMiiNameOff + i*2
		if off+1 >= commonDataMiiNameOff+commonDataMiiNameLen {
			break
		}
		b[off] = byte(u)
		b[off+1] = byte(u >> 8)
	}
	// Pseudo en jeu : un octet par caractere a l'offset 96, termine par 0.
	copy(b[commonDataNickOff:commonDataBlobLen-1], []byte(pseudo))
	return b
}

func TestCommonDataNamesLitLesDeuxNoms(t *testing.T) {
	mii, pseudo, ok := CommonDataNames(profilMK8("Yaacob", "ya'akov"))
	if !ok {
		t.Fatal("bloc conforme refusé")
	}
	if mii != "Yaacob" {
		t.Errorf("nom Mii : %q, attendu %q", mii, "Yaacob")
	}
	if pseudo != "ya'akov" {
		t.Errorf("pseudo : %q, attendu %q", pseudo, "ya'akov")
	}
}

// Les noms non latins doivent survivre : une part des joueurs en utilise, et un
// décodage qui les mutile rendrait la modération injuste pour eux seuls.
func TestCommonDataNamesGardeLeNonLatin(t *testing.T) {
	// Le nom Mii est en UTF-16 : c'est lui qui porte les alphabets non latins.
	mii, _, ok := CommonDataNames(profilMK8("アナキン", "anakin"))
	if !ok || mii != "アナキン" {
		t.Fatalf("nom Mii : %q ok=%v, attendu %q", mii, ok, "アナキン")
	}
}

// Le cas qui motive tout ce fichier : un client modifié qui ne dépose rien de
// conforme. On doit le distinguer d'un profil ordinaire, pas l'avaler.
func TestCommonDataNamesRefuseUnBlocNonConforme(t *testing.T) {
	// Sur 4 371 profils releves en production, TOUS font 132 octets : c'est la
	// taille qui distingue un bloc exploitable, pas une signature — les quatre
	// premiers octets prennent au moins quatre valeurs differentes.
	cas := map[string][]byte{
		"vide":       nil,
		"trop court": make([]byte, 10),
		"trop long":  make([]byte, 133),
		"131 octets": make([]byte, 131),
	}
	for nom, blob := range cas {
		if _, _, ok := CommonDataNames(blob); ok {
			t.Errorf("%s : accepté alors qu'il aurait dû être refusé", nom)
		}
	}
}

// Un pseudo vide est licite côté format : c'est un signal de modération, pas une
// erreur de décodage. Il doit ressortir comme chaîne vide avec ok=true.
func TestCommonDataNamesPseudoVide(t *testing.T) {
	_, pseudo, ok := CommonDataNames(profilMK8("Mii", ""))
	if !ok {
		t.Fatal("bloc conforme au pseudo vide refusé")
	}
	if pseudo != "" {
		t.Errorf("pseudo : %q, attendu vide", pseudo)
	}
}

// Le nom vient du client. Un pseudo démesuré ne doit pas pouvoir inonder un
// journal ni une interface de modération.
func TestCommonDataNamesBorneLaLongueur(t *testing.T) {
	_, pseudo, ok := CommonDataNames(profilMK8("Mii", strings.Repeat("A", 200)))
	if !ok {
		t.Fatal("bloc refusé")
	}
	if n := len([]rune(pseudo)); n > commonDataMaxNameRunes {
		t.Fatalf("pseudo de %d runes, la borne était %d", n, commonDataMaxNameRunes)
	}
}

// Un retour ligne dans un pseudo permettrait de fabriquer de fausses lignes dans
// les journaux, donc de faire accuser quelqu'un d'autre. Il doit disparaître.
func TestCommonDataNamesRetireLesCaracteresDeControle(t *testing.T) {
	_, pseudo, ok := CommonDataNames(profilMK8("Mii", "abc\ndef"))
	if !ok {
		t.Fatal("bloc refusé")
	}
	if strings.ContainsAny(pseudo, "\n\r\t") {
		t.Fatalf("pseudo %q contient encore un caractère de contrôle", pseudo)
	}
}

// Le nom Mii ne doit pas déborder sur le pseudo qui le suit immédiatement :
// c'est l'erreur qui ferait lire un nom pour un autre.
func TestCommonDataNamesNeDeborderPasSurLeChampSuivant(t *testing.T) {
	mii, pseudo, ok := CommonDataNames(profilMK8("AAAAAAAAAAA", "BBBBBBBB"))
	if !ok {
		t.Fatal("bloc refusé")
	}
	if strings.Contains(mii, "B") {
		t.Errorf("le nom Mii %q a mordu sur le pseudo", mii)
	}
	if strings.Contains(pseudo, "A") {
		t.Errorf("le pseudo %q a mordu sur le nom Mii", pseudo)
	}
}

// CommonDataNamesForPID part du PID, qui est authentifié. Un PID inconnu rend
// ok=false plutôt qu'un nom vide qu'on pourrait confondre avec un pseudo vide.
func TestCommonDataNamesForPIDInconnu(t *testing.T) {
	if _, _, ok := CommonDataNamesForPID(0xFFFFFFFFFFFF); ok {
		t.Fatal("un PID sans profil ne doit pas rendre ok=true")
	}
}
