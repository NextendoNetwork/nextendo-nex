package nex

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestTicketGrantingLoginEx(t *testing.T) {
	s := testSettings()
	const userPID = uint64(1800000000)
	const securePID = uint64(2)
	securePassword := "securepasswordplz1"
	sourceKey := []byte("nextendo-srckey1") // 16 bytes, handed back as pSourceKey

	cfg := &AuthConfig{
		Settings:         s,
		SecurePID:        securePID,
		SecurePassword:   securePassword,
		SecureStationURL: ParseStationURL("prudp:/address=1.2.3.4;port=60003"),
		ServerName:       "Nextendo",
		SessionKeyLength: 32,
		ResolveUser: func(username string, _ []byte) (uint64, []byte, bool) {
			if username == "player" {
				return userPID, sourceKey, true
			}
			return 0, nil, false
		},
	}
	handler := cfg.Handler()

	// LoginEx request: a username string (trailing extra data is ignored).
	reqBody := NewStreamOut(s)
	reqBody.String("player")
	req := NewRMCRequest(s, ProtocolTicketGranting, MethodLoginEx, 7, reqBody.Bytes())

	resp := handler(nil, req)
	if resp == nil || resp.IsError {
		t.Fatalf("no/failed response: %+v", resp)
	}

	in := NewStreamIn(resp.Body, s)
	retval := in.U32()
	gotPID := in.PID()
	ticket := in.Buffer()
	var rvcd RVConnectionData
	in.Extract(&rvcd)
	serverName := in.String()
	pSourceKey := in.String()
	if in.Err() != nil {
		t.Fatalf("decode response: %v", in.Err())
	}

	if retval != ResultCoreUnknown {
		t.Fatalf("retval %#x (want success %#x)", retval, ResultCoreUnknown)
	}
	if gotPID != userPID || serverName != "Nextendo" {
		t.Fatalf("pid=%d serverName=%q", gotPID, serverName)
	}
	if pSourceKey != hex.EncodeToString(sourceKey) {
		t.Fatalf("pSourceKey=%q", pSourceKey)
	}
	if rvcd.MainStation.GetInt("port") != 60003 || rvcd.MainStation.Get("address") != "1.2.3.4" {
		t.Fatalf("station=%s", rvcd.MainStation)
	}

	// The client decrypts the outer ticket with the returned source key.
	ct, err := DecryptClientTicket(ticket, sourceKey, s)
	if err != nil {
		t.Fatalf("client cannot decrypt ticket: %v", err)
	}
	if ct.Target != securePID {
		t.Fatalf("ticket target=%d", ct.Target)
	}
	// The secure server decrypts the internal data with its own key.
	targetKey := s.DeriveKey([]byte(securePassword), securePID)
	st, err := DecryptServerTicket(ct.Internal, targetKey, s)
	if err != nil {
		t.Fatalf("secure server cannot decrypt internal data: %v", err)
	}
	if st.Source != userPID {
		t.Fatalf("server ticket source=%d", st.Source)
	}
	// Both sides must agree on the session key.
	if !bytes.Equal(st.SessionKey, ct.SessionKey) {
		t.Fatal("session key mismatch between client ticket and server ticket")
	}
}

func TestTicketGrantingRejectsUnknownUser(t *testing.T) {
	s := testSettings()
	cfg := &AuthConfig{
		Settings:       s,
		SecurePID:      2,
		SecurePassword: "securepasswordplz1",
		ResolveUser: func(string, []byte) (uint64, []byte, bool) {
			return 0, nil, false
		},
	}

	body := NewStreamOut(s)
	body.String("intruder")
	resp := cfg.Handler()(nil, NewRMCRequest(s, ProtocolTicketGranting, MethodLoginEx, 1, body.Bytes()))

	retval := NewStreamIn(resp.Body, s).U32()
	if retval&ResultErrorMask == 0 {
		t.Fatalf("expected error retval, got %#x", retval)
	}
}

func TestTicketGrantingUnknownMethod(t *testing.T) {
	s := testSettings()
	cfg := &AuthConfig{Settings: s, ResolveUser: func(string, []byte) (uint64, []byte, bool) { return 0, nil, false }}
	resp := cfg.Handler()(nil, NewRMCRequest(s, ProtocolTicketGranting, 99, 1, nil))
	if !resp.IsError || resp.Result != (ResultCoreNotImplemented|ResultErrorMask) {
		t.Fatalf("expected NotImplemented error, got %+v", resp)
	}
}
