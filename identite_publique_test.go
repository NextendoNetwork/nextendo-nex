package nex

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
)

// Identifiants INVENTES : jamais un NSA reel dans un test.
const (
	testPIDInterne uint64 = 1800000042
	testNSA        uint64 = 0xF00DCAFE12345678 // > 2^63 : deborde un int, comme les vrais
)

func settingsIdentitePublique() *Settings {
	s := testSettings()
	s.PIDPublic = func(p uint64) uint64 {
		if p == testPIDInterne {
			return testNSA
		}
		return p
	}
	s.PIDInterne = func(p uint64) uint64 {
		if p == testNSA {
			return testPIDInterne
		}
		return p
	}
	return s
}

func TestPIDSansGancheInchange(t *testing.T) {
	out := NewStreamOut(testSettings())
	out.PID(testPIDInterne)
	want := make([]byte, 8)
	binary.LittleEndian.PutUint64(want, testPIDInterne)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("sans gancho, PID() doit ecrire le PID tel quel: %x", out.Bytes())
	}
}

func TestPIDSortPublicEtRentreInterne(t *testing.T) {
	s := settingsIdentitePublique()
	out := NewStreamOut(s)
	out.PID(testPIDInterne)
	if got := binary.LittleEndian.Uint64(out.Bytes()); got != testNSA {
		t.Fatalf("PID sortant = %d, attendu le NSA %d", got, testNSA)
	}
	if got := NewStreamIn(out.Bytes(), s).PID(); got != testPIDInterne {
		t.Fatalf("PID entrant = %d, attendu le PID interne %d", got, testPIDInterne)
	}
}

// Le CONNECT : le ticket chiffre porte le PID interne, la console declare celui que l'Auth
// lui a donne (le NSA). L'egalite pid == ticket.Source doit tenir, et la connexion doit
// rester sur le PID interne.
func TestConnectAccepteLIdentitePublique(t *testing.T) {
	s := settingsIdentitePublique()
	secureKey := s.DeriveKey([]byte("securepasswordplz1"), 2)
	session := bytes.Repeat([]byte{0x5A}, s.KerberosKeySize)
	ticket, err := (&ServerTicket{Timestamp: NowDateTime(), Source: testPIDInterne, SessionKey: session}).Encrypt(secureKey, s)
	if err != nil {
		t.Fatal(err)
	}

	// Ce que la console chiffre : SON pid (le public), cid, check. Ecrit sans gancho.
	req := make([]byte, 16)
	binary.LittleEndian.PutUint64(req[0:], testNSA)
	binary.LittleEndian.PutUint32(req[8:], 7)
	binary.LittleEndian.PutUint32(req[12:], 41)
	payload := NewStreamOut(s)
	payload.Buffer(ticket)
	payload.Buffer(kerberosEncrypt(session, req))

	c := &Connection{Settings: s, Endpoint: &Endpoint{Secure: true, SecureKey: secureKey}}
	if _, err := c.processLoginRequest(payload.Bytes()); err != nil {
		t.Fatalf("CONNECT refuse: %v", err)
	}
	if c.PID != testPIDInterne {
		t.Fatalf("conn.PID = %d, attendu le PID interne", c.PID)
	}
}

// Une console qui declare un AUTRE identifiant que celui du ticket reste refusee.
func TestConnectRefuseUnAutreIdentifiant(t *testing.T) {
	s := settingsIdentitePublique()
	secureKey := s.DeriveKey([]byte("securepasswordplz1"), 2)
	session := bytes.Repeat([]byte{0x5A}, s.KerberosKeySize)
	ticket, _ := (&ServerTicket{Timestamp: NowDateTime(), Source: testPIDInterne, SessionKey: session}).Encrypt(secureKey, s)
	req := make([]byte, 16)
	binary.LittleEndian.PutUint64(req[0:], testNSA+1)
	payload := NewStreamOut(s)
	payload.Buffer(ticket)
	payload.Buffer(kerberosEncrypt(session, req))
	c := &Connection{Settings: s, Endpoint: &Endpoint{Secure: true, SecureKey: secureKey}}
	if _, err := c.processLoginRequest(payload.Bytes()); err == nil {
		t.Fatal("un identifiant different du ticket doit etre refuse")
	}
}

func TestStationURLPIDPublicSansSigne(t *testing.T) {
	s := settingsIdentitePublique()
	c := &Connection{Settings: s, PID: testPIDInterne, RemoteAddr: "203.0.113.5:4000"}
	in := NewStreamOut(s)
	WriteList(in, []*StationURL{ParseStationURL("prudp:/address=192.168.1.2;port=5000;type=2")}, func(o *StreamOut, u *StationURL) { o.StationURL(u) })
	resp := handleRegister(c, &RMCMessage{Body: in.Bytes()}, SecureConnectionConfig{})
	if resp == nil {
		t.Fatal("pas de reponse")
	}
	for _, u := range c.Stations() {
		if !strings.Contains(u.String(), "PID="+strconv.FormatUint(testNSA, 10)+";") {
			t.Fatalf("station sans le PID public non signe: %s", u.String())
		}
	}
}

func TestNotificationParticipationParam2Public(t *testing.T) {
	s := settingsIdentitePublique()
	for _, typ := range []uint32{NotificationParticipate, NotificationOwnershipChanged, NotificationHostChanged} {
		out := NewStreamOut(s)
		out.Add(&NotificationEvent{PIDSource: testPIDInterne, Type: typ, Param1: 9, Param2: testPIDInterne})
		b := out.Bytes()
		// en-tete 5 + PIDSource 8 + Type 4 + Param1 8 -> Param2
		if got := binary.LittleEndian.Uint64(b[5+8+4+8:]); got != testNSA {
			t.Fatalf("type %d : Param2 = %d, attendu le NSA", typ, got)
		}
	}
	// Un evenement dont Param2 n'est pas un joueur garde sa valeur.
	out := NewStreamOut(s)
	out.Add(&NotificationEvent{PIDSource: testPIDInterne, Type: NotificationGatheringUnregistered, Param2: testPIDInterne})
	if got := binary.LittleEndian.Uint64(out.Bytes()[5+8+4+8:]); got != testPIDInterne {
		t.Fatalf("109000 : Param2 traduit a tort: %d", got)
	}
}
