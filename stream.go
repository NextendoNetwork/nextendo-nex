package nex

import (
	"encoding/binary"
	"io"
	"math"
)

// StreamOut serializes NEX values little-endian into an in-memory buffer.
// A zero StreamOut is not usable; construct one with NewStreamOut so it carries
// the Settings that drive version-dependent encoding (PID size, struct headers).
type StreamOut struct {
	Settings *Settings
	buf      []byte
}

// NewStreamOut returns an empty output stream bound to the given settings.
func NewStreamOut(s *Settings) *StreamOut { return &StreamOut{Settings: s} }

// Bytes returns the bytes written so far (not a copy).
func (s *StreamOut) Bytes() []byte { return s.buf }

// Write appends raw bytes.
func (s *StreamOut) Write(b []byte) { s.buf = append(s.buf, b...) }

func (s *StreamOut) U8(v uint8)   { s.buf = append(s.buf, v) }
func (s *StreamOut) U16(v uint16) { s.buf = binary.LittleEndian.AppendUint16(s.buf, v) }
func (s *StreamOut) U32(v uint32) { s.buf = binary.LittleEndian.AppendUint32(s.buf, v) }
func (s *StreamOut) U64(v uint64) { s.buf = binary.LittleEndian.AppendUint64(s.buf, v) }
func (s *StreamOut) S8(v int8)    { s.U8(uint8(v)) }
func (s *StreamOut) S16(v int16)  { s.U16(uint16(v)) }
func (s *StreamOut) S32(v int32)  { s.U32(uint32(v)) }
func (s *StreamOut) S64(v int64)  { s.U64(uint64(v)) }

func (s *StreamOut) Float(v float32)  { s.U32(math.Float32bits(v)) }
func (s *StreamOut) Double(v float64) { s.U64(math.Float64bits(v)) }

func (s *StreamOut) Bool(v bool) {
	if v {
		s.U8(1)
	} else {
		s.U8(0)
	}
}

// PID writes a principal ID, 4 or 8 bytes per the settings.
func (s *StreamOut) PID(v uint64) {
	if s.Settings.PIDSize == 8 {
		s.U64(v)
	} else {
		s.U32(uint32(v))
	}
}

// String writes a NEX String: a u16 byte length (including the null terminator)
// followed by the UTF-8 bytes and a trailing 0x00.
func (s *StreamOut) String(str string) {
	data := make([]byte, 0, len(str)+1)
	data = append(data, str...)
	data = append(data, 0)
	s.U16(uint16(len(data)))
	s.Write(data)
}

// Buffer writes a u32-length-prefixed byte blob.
func (s *StreamOut) Buffer(b []byte) { s.U32(uint32(len(b))); s.Write(b) }

// QBuffer writes a u16-length-prefixed byte blob.
func (s *StreamOut) QBuffer(b []byte) { s.U16(uint16(len(b))); s.Write(b) }

// Result writes a 32-bit NEX result code.
func (s *StreamOut) Result(code uint32) { s.U32(code) }

// DateTime writes a packed NEX DateTime (u64).
func (s *StreamOut) DateTime(v uint64) { s.U64(v) }

// StationURL writes a station URL as a NEX String.
func (s *StreamOut) StationURL(u *StationURL) { s.String(u.String()) }

// Add writes a NEX Structure, framing each hierarchy level with a version byte
// and length prefix when struct headers are enabled.
func (s *StreamOut) Add(st Structure) { WriteStructure(s, st) }

// WriteList writes a NEX List<T>: a u32 element count followed by each element.
func WriteList[T any](s *StreamOut, list []T, f func(*StreamOut, T)) {
	s.U32(uint32(len(list)))
	for i := range list {
		f(s, list[i])
	}
}

// WriteMap writes a NEX Map<K,V>: a u32 pair count followed by key/value pairs.
// Go map iteration order is randomized; callers needing a byte-stable output
// (e.g. to reproduce a capture) must serialize from an ordered source instead.
func WriteMap[K comparable, V any](s *StreamOut, m map[K]V, kf func(*StreamOut, K), vf func(*StreamOut, V)) {
	s.U32(uint32(len(m)))
	for k, v := range m {
		kf(s, k)
		vf(s, v)
	}
}

// StreamIn deserializes NEX values little-endian. On underflow it records an
// error (retrievable with Err) and subsequent reads return zero values, so a
// decode path can run to completion and be validated once at the end.
type StreamIn struct {
	Settings *Settings
	data     []byte
	pos      int
	err      error
}

// NewStreamIn returns an input stream over data bound to the given settings.
func NewStreamIn(data []byte, s *Settings) *StreamIn {
	return &StreamIn{data: data, Settings: s}
}

// Err returns the first read error encountered, or nil.
func (s *StreamIn) Err() error { return s.err }

// EOF reports whether the read cursor is at or past the end.
func (s *StreamIn) EOF() bool { return s.pos >= len(s.data) }

// Remaining returns the number of unread bytes.
func (s *StreamIn) Remaining() int { return len(s.data) - s.pos }

// ReadAll returns all remaining bytes (empty, never nil, at EOF).
func (s *StreamIn) ReadAll() []byte {
	b := s.Read(s.Remaining())
	if b == nil {
		return []byte{}
	}
	return b
}

// Read returns the next n bytes, or nil (setting the error) on underflow. The
// returned slice aliases the underlying buffer; do not mutate it.
func (s *StreamIn) Read(n int) []byte {
	if s.err != nil || n < 0 || s.pos+n > len(s.data) {
		if s.err == nil && n != 0 {
			s.err = io.ErrUnexpectedEOF
		}
		return nil
	}
	b := s.data[s.pos : s.pos+n]
	s.pos += n
	return b
}

func (s *StreamIn) U8() uint8 {
	b := s.Read(1)
	if len(b) < 1 {
		return 0
	}
	return b[0]
}

func (s *StreamIn) U16() uint16 {
	b := s.Read(2)
	if len(b) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (s *StreamIn) U32() uint32 {
	b := s.Read(4)
	if len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (s *StreamIn) U64() uint64 {
	b := s.Read(8)
	if len(b) < 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (s *StreamIn) S8() int8   { return int8(s.U8()) }
func (s *StreamIn) S16() int16 { return int16(s.U16()) }
func (s *StreamIn) S32() int32 { return int32(s.U32()) }
func (s *StreamIn) S64() int64 { return int64(s.U64()) }

func (s *StreamIn) Float() float32  { return math.Float32frombits(s.U32()) }
func (s *StreamIn) Double() float64 { return math.Float64frombits(s.U64()) }
func (s *StreamIn) Bool() bool      { return s.U8() != 0 }

// PID reads a principal ID, 4 or 8 bytes per the settings.
func (s *StreamIn) PID() uint64 {
	if s.Settings.PIDSize == 8 {
		return s.U64()
	}
	return uint64(s.U32())
}

// String reads a NEX String and strips the trailing null terminator.
func (s *StreamIn) String() string {
	length := int(s.U16())
	if length == 0 {
		return ""
	}
	b := s.Read(length)
	if len(b) < length {
		return ""
	}
	if b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return string(b)
}

// Buffer reads a u32-length-prefixed byte blob.
func (s *StreamIn) Buffer() []byte { return s.Read(int(s.U32())) }

// QBuffer reads a u16-length-prefixed byte blob.
func (s *StreamIn) QBuffer() []byte { return s.Read(int(s.U16())) }

// Result reads a 32-bit NEX result code.
func (s *StreamIn) Result() uint32 { return s.U32() }

// DateTime reads a packed NEX DateTime (u64).
func (s *StreamIn) DateTime() uint64 { return s.U64() }

// StationURLValue reads a station URL from a NEX String.
func (s *StreamIn) StationURLValue() *StationURL { return ParseStationURL(s.String()) }

// Substream reads a u32-length-prefixed blob and returns a stream over it.
func (s *StreamIn) Substream() *StreamIn { return NewStreamIn(s.Buffer(), s.Settings) }

// Extract reads a NEX Structure into st.
func (s *StreamIn) Extract(st Structure) { ReadStructure(s, st) }

// ReadList reads a NEX List<T>. A count larger than the remaining bytes is
// treated as corruption (each element occupies at least one byte).
func ReadList[T any](s *StreamIn, f func(*StreamIn) T) []T {
	n := int(s.U32())
	if n < 0 || n > s.Remaining() {
		if s.err == nil {
			s.err = io.ErrUnexpectedEOF
		}
		return nil
	}
	out := make([]T, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, f(s))
	}
	return out
}

// ReadMap reads a NEX Map<K,V>.
func ReadMap[K comparable, V any](s *StreamIn, kf func(*StreamIn) K, vf func(*StreamIn) V) map[K]V {
	n := int(s.U32())
	if n < 0 || n > s.Remaining() {
		if s.err == nil {
			s.err = io.ErrUnexpectedEOF
		}
		return nil
	}
	m := make(map[K]V, n)
	for i := 0; i < n; i++ {
		k := kf(s)
		m[k] = vf(s)
	}
	return m
}
