package nex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

// NexTokenFromLoginExtraData extracts the signed "nx2." Nextendo token that the
// Nextendo emulator embeds in the BAAS id_token it forwards inside a NEX login's
// extraData. The extraData is an "AuthenticationInfo" DataHolder whose Token field
// is a JWT; the emulator (>= 1.7.1) adds the nx2 token as a custom "nnex" claim
// (see the emulator's ManagerServer.GenerateIdToken). We locate the JWT — a
// contiguous base64url+dot run beginning "eyJ" — decode its payload segment, and
// read the claim.
//
// Returning ("", false) means no bound token was present: a build older than 1.7.1,
// a real CFW Switch (which sends a genuine BAAS id_token, no nnex), or a forged login
// with no proof. Callers decide whether that is acceptable (shadow vs enforce).
//
// This deliberately SCANS for the JWT rather than parsing the DataHolder structure,
// because the exact framing varies across NEX versions/titles. A tolerant scan means
// a legitimate login is never rejected over a framing mismatch — only over a missing
// or wrong proof.
func NexTokenFromLoginExtraData(extraData []byte) (string, bool) {
	i := bytes.Index(extraData, []byte("eyJ"))
	if i < 0 {
		return "", false
	}
	j := i
	for j < len(extraData) && isJWTByte(extraData[j]) {
		j++
	}
	segments := bytes.Split(extraData[i:j], []byte("."))
	if len(segments) < 2 {
		return "", false
	}
	// JWT segments are unpadded base64url; TrimRight tolerates a padded encoder too.
	payload, err := base64.RawURLEncoding.DecodeString(string(bytes.TrimRight(segments[1], "=")))
	if err != nil {
		return "", false
	}
	var claims struct {
		Nnex string `json:"nnex"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Nnex == "" {
		return "", false
	}
	return claims.Nnex, true
}

// isJWTByte reports whether b is a valid character inside a compact JWS/JWT
// (base64url alphabet plus the '.' segment separator).
func isJWTByte(b byte) bool {
	return b == '.' || b == '-' || b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z')
}
