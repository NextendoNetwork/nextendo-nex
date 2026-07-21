package nex

import (
	"bytes"
	"testing"
)

func TestDeriveKeyNewDeterministic(t *testing.T) {
	pw := []byte("securepasswordplz1")
	a := testSettings().DeriveKey(pw, 2)
	b := testSettings().DeriveKey(pw, 2)
	if !bytes.Equal(a, b) {
		t.Fatal("derive key not deterministic")
	}
	if len(a) != 16 {
		t.Fatalf("derived key length: %d", len(a))
	}
	// A different PID must produce a different key.
	if bytes.Equal(a, testSettings().DeriveKey(pw, 3)) {
		t.Fatal("derived key does not depend on pid")
	}
}

func TestKerberosEncryptRoundTrip(t *testing.T) {
	key := testSettings().DeriveKey([]byte("abcdefghijklmnop"), 1800000000)
	data := []byte("hello nextendo kerberos payload")

	enc := kerberosEncrypt(key, data)
	dec, err := kerberosDecrypt(key, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(dec, data) {
		t.Fatalf("round-trip mismatch: %q", dec)
	}
	// Tampering must be detected by the HMAC checksum.
	enc[0] ^= 0xFF
	if _, err := kerberosDecrypt(key, enc); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestClientTicketRoundTrip(t *testing.T) {
	s := testSettings()
	userKey := s.DeriveKey([]byte("abcdefghijklmnop"), 1800000000)
	session := bytes.Repeat([]byte{0xAB}, s.KerberosKeySize)

	ticket := &ClientTicket{SessionKey: session, Target: 2, Internal: []byte{0x11, 0x22, 0x33}}
	enc, err := ticket.Encrypt(userKey, s)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := DecryptClientTicket(enc, userKey, s)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got.Target != 2 || !bytes.Equal(got.SessionKey, session) || !bytes.Equal(got.Internal, []byte{0x11, 0x22, 0x33}) {
		t.Fatalf("client ticket round-trip: %+v", got)
	}
}

func TestServerTicketRoundTrip(t *testing.T) {
	s := testSettings()
	secureKey := s.DeriveKey([]byte("securepasswordplz1"), 2)
	session := bytes.Repeat([]byte{0xCD}, s.KerberosKeySize)

	ticket := &ServerTicket{Timestamp: MakeDateTime(2026, 7, 13, 12, 0, 0), Source: 1800000000, SessionKey: session}
	enc, err := ticket.Encrypt(secureKey, s)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := DecryptServerTicket(enc, secureKey, s)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got.Source != 1800000000 || got.Timestamp != ticket.Timestamp || !bytes.Equal(got.SessionKey, session) {
		t.Fatalf("server ticket round-trip: %+v", got)
	}
	// A wrong server key must fail the checksum.
	if _, err := DecryptServerTicket(enc, s.DeriveKey([]byte("wrong"), 2), s); err == nil {
		t.Fatal("server ticket decrypted with wrong key")
	}
}
