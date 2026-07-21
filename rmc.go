package nex

import (
	"encoding/binary"
	"fmt"
)

// RMC message modes.
const (
	RMCRequest  = 0
	RMCResponse = 1
)

// RMCMessage is one Remote Method Call request or response. On the wire it is a
// u32 length prefix followed by:
//
//	protocol byte (high bit 0x80 = request; 0x7F escape → u16 protocol follows)
//	request:  u32 callID, u32 method, body
//	response: bool success;
//	          success → u32 callID, u32 (method | 0x8000), body
//	          error   → u32 result, u32 callID
type RMCMessage struct {
	Settings *Settings
	Mode     int
	Protocol uint16
	Method   uint32
	CallID   uint32
	Result   uint32 // error responses only; carries the error bit
	Body     []byte
	IsError  bool
}

// NewRMCRequest builds a request message.
func NewRMCRequest(s *Settings, protocol uint16, method, callID uint32, body []byte) *RMCMessage {
	return &RMCMessage{Settings: s, Mode: RMCRequest, Protocol: protocol, Method: method, CallID: callID, Body: body}
}

// NewRMCSuccess builds a success response.
func NewRMCSuccess(s *Settings, protocol uint16, method, callID uint32, body []byte) *RMCMessage {
	return &RMCMessage{Settings: s, Mode: RMCResponse, Protocol: protocol, Method: method, CallID: callID, Body: body}
}

// NewRMCError builds an error response. The error bit is forced on.
func NewRMCError(s *Settings, protocol uint16, callID, errorCode uint32) *RMCMessage {
	return &RMCMessage{Settings: s, Mode: RMCResponse, Protocol: protocol, CallID: callID, Result: errorCode | ResultErrorMask, IsError: true}
}

// Encode serializes the message including its u32 length prefix.
func (m *RMCMessage) Encode() []byte {
	s := NewStreamOut(m.Settings)

	var flag uint8
	if m.Mode == RMCRequest {
		flag = 0x80
	}
	if m.Protocol < 0x80 {
		s.U8(uint8(m.Protocol) | flag)
	} else {
		s.U8(0x7F | flag)
		s.U16(m.Protocol)
	}

	switch {
	case m.Mode == RMCRequest:
		s.U32(m.CallID)
		s.U32(m.Method)
		s.Write(m.Body)
	case m.IsError:
		s.Bool(false)
		s.U32(m.Result | ResultErrorMask)
		s.U32(m.CallID)
	default:
		s.Bool(true)
		s.U32(m.CallID)
		s.U32(m.Method | 0x8000)
		s.Write(m.Body)
	}

	payload := s.Bytes()
	out := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(payload)))
	copy(out[4:], payload)
	return out
}

// ParseRMC decodes an RMC message (length prefix included).
func ParseRMC(s *Settings, data []byte) (*RMCMessage, error) {
	in := NewStreamIn(data, s)
	length := in.U32()
	if int(length) != len(data)-4 {
		return nil, fmt.Errorf("rmc: size mismatch (header=%d, actual=%d)", length, len(data)-4)
	}

	m := &RMCMessage{Settings: s}
	proto := in.U8()
	m.Protocol = uint16(proto &^ 0x80)
	if m.Protocol == 0x7F {
		m.Protocol = in.U16()
	}

	if proto&0x80 != 0 {
		m.Mode = RMCRequest
		m.CallID = in.U32()
		m.Method = in.U32()
		m.Body = in.ReadAll()
	} else {
		m.Mode = RMCResponse
		if in.Bool() {
			m.CallID = in.U32()
			m.Method = in.U32() &^ 0x8000
			m.Body = in.ReadAll()
		} else {
			m.IsError = true
			m.Result = in.U32()
			m.CallID = in.U32()
		}
	}
	return m, in.Err()
}

// notImplemented refuses a method AND says so.
//
// Every one of these used to be a silent NewRMCError: the server answered "no", the game
// gave up, and nothing on our side recorded which method it was. That turns a five-minute
// diagnosis into log archaeology across a client capture — it is how UpdateMatchmakeSessionPart
// and AcquireNexUniqueIDWithPassword both stayed hidden for weeks, each breaking a subset of
// players. A refusal is the single most interesting thing this server does; it gets a line.
func notImplemented(conn *Connection, proto uint16, req *RMCMessage) *RMCMessage {
	fmt.Printf("[NEX] UNHANDLED proto=0x%x method=%d (0x%x) pid=%d callID=%d bodyLen=%d\n",
		proto, req.Method, req.Method, conn.PID, req.CallID, len(req.Body))

	return NewRMCError(conn.Settings, proto, req.CallID, ResultCoreNotImplemented)
}
