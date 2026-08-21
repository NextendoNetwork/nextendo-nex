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
	b := make([]byte, 132)
	copy(b, commonDataMagic)
	put := func(off int, s string, max int) {
		for i, u := range utf16.Encode([]rune(s)) {
			if off+i*2+1 >= len(b) || i*2 >= max {
				return
			}
			b[off+i*2] = byte(u)
			b[off+i*2+1] = byte(u >> 8)
		}
	}
	put(commonDataMiiNameOff, mii, commonDataMiiNameLen)
	put(commonDataNickOff, pseudo, 40)
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
	_, pseudo, ok := CommonDataNames(profilMK8("Mii", "アナキン"))
	if !ok || pseudo != "アナキン" {
		t.Fatalf("pseudo : %q ok=%v, attendu %q", pseudo, ok, "アナキン")
	}
}

// Le cas qui motive tout ce fichier : un client modifié qui ne dépose rien de
// conforme. On doit le distinguer d'un profil ordinaire, pas l'avaler.
func TestCommonDataNamesRefuseUnBlocNonConforme(t *testing.T) {
	cas := map[string][]byte{
		"vide":              nil,
		"trop court":        make([]byte, 10),
		"sans signature":    make([]byte, 132),
		"signature altérée": append([]byte{0x52, 0x46, 0x01, 0x00}, make([]byte, 128)...),
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
