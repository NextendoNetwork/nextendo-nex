package nex

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// buildIDToken builds a JWT shaped like the one the Nextendo emulator's
// ManagerServer.GenerateIdToken produces. When nnex != "", it carries the signed
// Nextendo token in a custom "nnex" claim (as the >= 1.7.1 emulator does); when
// empty, the claim is omitted entirely (a real CFW Switch or a pre-1.7.1 build).
func buildIDToken(nnex string) string {
	b64 := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	header := b64(map[string]any{"alg": "RS256", "kid": "nextendo-baas-key-1", "typ": "id_token"})
	claims := map[string]any{
		"sub": "0123456789abcdef0123456789abcdef",
		"iss": "https://example.invalid/baas",
		"aud": "0000000000000000",
		"hm":  true,
	}
	if nnex != "" {
		claims["nnex"] = nnex
	}
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature-not-verified-here"))
	return header + "." + b64(claims) + "." + sig
}

// wrapAsExtraData embeds a JWT the way it appears in a NEX login's extraData: an
// "AuthenticationInfo" DataHolder whose Token field is a NEX String, surrounded by
// the structure header and the other AuthenticationInfo fields.
func wrapAsExtraData(jwt string) []byte {
	settings := NewSwitchSettings("testkey0", 40000)
	s := NewStreamOut(settings)
	s.String("AuthenticationInfo") // DataHolder name
	body := NewStreamOut(settings)
	body.U8(1)             // structure version
	body.U32(0)            // structure content length (placeholder)
	body.String(jwt)       // Token (the id_token)
	body.U32(3)            // NGSVersion
	body.U8(1)             // TokenType
	body.U32(40000)        // ServerVersion
	s.Buffer(body.Bytes()) // DataHolder body, u32-length-prefixed
	return s.Bytes()
}

func TestNexTokenFromLoginExtraData_Extracts(t *testing.T) {
	want := "nx2.MTgwMDAwMDA0Mi5Nb2hhLjk5OTk5OTk5OTk.QUJDREVG"
	extra := wrapAsExtraData(buildIDToken(want))
	got, ok := NexTokenFromLoginExtraData(extra)
	if !ok || got != want {
		t.Fatalf("nnex extraction: want %q ok, got %q ok=%v", want, got, ok)
	}
}

func TestNexTokenFromLoginExtraData_NoClaim(t *testing.T) {
	// Real CFW Switch or a pre-1.7.1 build: a valid id_token but no nnex binding.
	extra := wrapAsExtraData(buildIDToken(""))
	if tok, ok := NexTokenFromLoginExtraData(extra); ok {
		t.Fatalf("expected no bound token, got %q", tok)
	}
}

func TestNexTokenFromLoginExtraData_NoJWT(t *testing.T) {
	if _, ok := NexTokenFromLoginExtraData(nil); ok {
		t.Fatal("expected false on nil extraData")
	}
	if _, ok := NexTokenFromLoginExtraData([]byte("\x00\x12no json web token here")); ok {
		t.Fatal("expected false when no JWT present")
	}
}
