package nex

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"errors"
)

// DeriveKey derives a principal's RC4 key from its password and PID, per the
// settings' key-derivation mode.
func (s *Settings) DeriveKey(password []byte, pid uint64) []byte {
	if s.KerberosKeyDerivation == KeyDerivationNew {
		return deriveKeyNew(password, pid, 1, 1)
	}
	return deriveKeyOld(password, pid, 65000, 1024)
}

// deriveKeyOld: md5(password) iterated (baseCount + pid%pidCount) times.
func deriveKeyOld(password []byte, pid uint64, baseCount, pidCount int) []byte {
	key := append([]byte(nil), password...)
	n := baseCount + int(pid%uint64(pidCount))
	for i := 0; i < n; i++ {
		sum := md5.Sum(key)
		key = sum[:]
	}
	return key
}

// deriveKeyNew: md5(password) baseCount times, append pid as u64 LE, md5 pidCount times.
func deriveKeyNew(password []byte, pid uint64, baseCount, pidCount int) []byte {
	key := append([]byte(nil), password...)
	for i := 0; i < baseCount; i++ {
		sum := md5.Sum(key)
		key = sum[:]
	}
	buf := make([]byte, len(key)+8)
	copy(buf, key)
	binary.LittleEndian.PutUint64(buf[len(key):], pid)
	key = buf
	for i := 0; i < pidCount; i++ {
		sum := md5.Sum(key)
		key = sum[:]
	}
	return key
}

func hmacMD5(key, data []byte) []byte {
	h := hmac.New(md5.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func md5Sum(b []byte) []byte {
	s := md5.Sum(b)
	return s[:]
}

// kerberosEncrypt RC4-encrypts data with key and appends a 16-byte HMAC-MD5 of
// the ciphertext (keyed with the same key).
//
// NOTE: RC4 is cryptographically broken (fluhrer-Mantin-Shamir attack, 2001+).
// It is used here solely for NEX-protocol compatibility — Nintendo's spec
// mandates RC4 for Kerberos ticket encryption. Do not reuse this cipher for
// any other purpose. The HMAC-MD5 checksum mitigates the worst of RC4's
// malleability but does not fix its fundamental weaknesses.
func kerberosEncrypt(key, data []byte) []byte {
	c, _ := rc4.NewCipher(key)
	enc := make([]byte, len(data))
	c.XORKeyStream(enc, data)
	return append(enc, hmacMD5(key, enc)...)
}

// kerberosDecrypt verifies the trailing HMAC-MD5 checksum then RC4-decrypts.
func kerberosDecrypt(key, buffer []byte) ([]byte, error) {
	if len(buffer) < 16 {
		return nil, errors.New("kerberos: buffer too short")
	}
	data := buffer[:len(buffer)-16]
	checksum := buffer[len(buffer)-16:]
	if !hmac.Equal(checksum, hmacMD5(key, data)) {
		return nil, errors.New("kerberos: invalid checksum (wrong password)")
	}
	c, _ := rc4.NewCipher(key)
	dec := make([]byte, len(data))
	c.XORKeyStream(dec, data)
	return dec, nil
}

// ClientTicket is the ticket the client receives, encrypted with the client's
// own key: {session key, target PID, internal (the ServerTicket blob)}.
type ClientTicket struct {
	SessionKey []byte
	Target     uint64
	Internal   []byte
}

// Encrypt serializes and encrypts the ticket with the client's key.
func (t *ClientTicket) Encrypt(key []byte, s *Settings) ([]byte, error) {
	if len(t.SessionKey) != s.KerberosKeySize {
		return nil, errors.New("kerberos: incorrect session key size")
	}
	out := NewStreamOut(s)
	out.Write(t.SessionKey)
	out.PID(t.Target)
	out.Buffer(t.Internal)
	return kerberosEncrypt(key, out.Bytes()), nil
}

// DecryptClientTicket decrypts a client ticket with the client's key.
func DecryptClientTicket(data, key []byte, s *Settings) (*ClientTicket, error) {
	dec, err := kerberosDecrypt(key, data)
	if err != nil {
		return nil, err
	}
	in := NewStreamIn(dec, s)
	t := &ClientTicket{
		SessionKey: append([]byte(nil), in.Read(s.KerberosKeySize)...),
		Target:     in.PID(),
		Internal:   append([]byte(nil), in.Buffer()...),
	}
	return t, in.Err()
}

// ServerTicket is the blob the secure server decrypts (with its own key) to
// recover the session: {timestamp, source PID, session key}. Ticket version 1
// wraps it with a random per-ticket key mixed into the server key.
type ServerTicket struct {
	Timestamp  DateTime
	Source     uint64
	SessionKey []byte
}

// Encrypt serializes and encrypts the ticket with the secure server's key.
func (t *ServerTicket) Encrypt(key []byte, s *Settings) ([]byte, error) {
	if len(t.SessionKey) != s.KerberosKeySize {
		return nil, errors.New("kerberos: incorrect session key size")
	}
	body := NewStreamOut(s)
	body.DateTime(t.Timestamp.Value())
	body.PID(t.Source)
	body.Write(t.SessionKey)
	data := body.Bytes()

	if s.KerberosTicketVersion == 1 {
		ticketKey := make([]byte, 16)
		if _, err := rand.Read(ticketKey); err != nil {
			return nil, err
		}
		finalKey := md5Sum(append(append([]byte(nil), key...), ticketKey...))
		enc := kerberosEncrypt(finalKey, data)

		out := NewStreamOut(s)
		out.Buffer(ticketKey)
		out.Buffer(enc)
		return out.Bytes(), nil
	}
	return kerberosEncrypt(key, data), nil
}

// DecryptServerTicket decrypts a server ticket with the secure server's key.
func DecryptServerTicket(data, key []byte, s *Settings) (*ServerTicket, error) {
	if s.KerberosTicketVersion == 1 {
		in := NewStreamIn(data, s)
		ticketKey := in.Buffer()
		inner := in.Buffer()
		if in.Err() != nil {
			return nil, in.Err()
		}
		key = md5Sum(append(append([]byte(nil), key...), ticketKey...))
		data = inner
	}
	dec, err := kerberosDecrypt(key, data)
	if err != nil {
		return nil, err
	}
	in := NewStreamIn(dec, s)
	t := &ServerTicket{
		Timestamp:  DateTime(in.DateTime()),
		Source:     in.PID(),
		SessionKey: append([]byte(nil), in.Read(s.KerberosKeySize)...),
	}
	return t, in.Err()
}
