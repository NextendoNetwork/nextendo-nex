package nex

import (
	"bytes"
	"testing"
)

func TestRMCRequestRoundTrip(t *testing.T) {
	body := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	msg := NewRMCRequest(testSettings(), 0x6D, 1, 5, body)
	enc := msg.Encode()

	got, err := ParseRMC(testSettings(), enc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Mode != RMCRequest || got.Protocol != 0x6D || got.Method != 1 || got.CallID != 5 {
		t.Fatalf("request fields: %+v", got)
	}
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("request body: % x", got.Body)
	}
}

func TestRMCSuccessRoundTrip(t *testing.T) {
	body := []byte{0x01, 0x02, 0x03}
	msg := NewRMCSuccess(testSettings(), 0x6D, 7, 9, body)
	got, err := ParseRMC(testSettings(), msg.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The method's 0x8000 success bit must be stripped back off on decode.
	if got.Mode != RMCResponse || got.IsError || got.Method != 7 || got.CallID != 9 {
		t.Fatalf("success fields: %+v", got)
	}
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("success body: % x", got.Body)
	}
}

func TestRMCErrorRoundTrip(t *testing.T) {
	// 0x00010001 = Core::NotImplemented style code; error bit forced on.
	msg := NewRMCError(testSettings(), 0x6D, 11, 0x00010001)
	got, err := ParseRMC(testSettings(), msg.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Mode != RMCResponse || !got.IsError || got.CallID != 11 {
		t.Fatalf("error fields: %+v", got)
	}
	if got.Result&ResultErrorMask == 0 {
		t.Fatalf("error bit not set: %#x", got.Result)
	}
}

// TestRMCExtendedProtocol exercises the 0x7F escape for protocol ids >= 0x80.
func TestRMCExtendedProtocol(t *testing.T) {
	msg := NewRMCRequest(testSettings(), 200, 3, 1, nil)
	got, err := ParseRMC(testSettings(), msg.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Protocol != 200 || got.Method != 3 {
		t.Fatalf("extended protocol: %+v", got)
	}
}
