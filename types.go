package nex

import (
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Structure framework
// ---------------------------------------------------------------------------

// Structure is a NEX-serializable object. Levels returns its class hierarchy
// from base to derived; each Level serializes only that level's own fields.
// When struct headers are enabled each level is framed on the wire as
// [u8 version][u32 length][body]. This mirrors how the game encodes inherited
// structures (e.g. MatchmakeSession is a Gathering plus its own fields), which
// is what makes the output byte-exact.
type Structure interface {
	Levels() []Level
}

// Level is one class level of a Structure hierarchy.
type Level struct {
	Version uint8
	Save    func(out *StreamOut)
	Load    func(in *StreamIn)
}

// WriteStructure serializes st, framing each level when struct headers are on.
func WriteStructure(out *StreamOut, st Structure) {
	for _, lvl := range st.Levels() {
		if out.Settings.StructHeader {
			sub := NewStreamOut(out.Settings)
			if lvl.Save != nil {
				lvl.Save(sub)
			}
			out.U8(lvl.Version)
			out.Buffer(sub.Bytes())
		} else if lvl.Save != nil {
			lvl.Save(out)
		}
	}
}

// ReadStructure deserializes into st. The per-level version byte is read and
// accepted (a higher-than-expected version is tolerated, matching NEX clients).
func ReadStructure(in *StreamIn, st Structure) {
	for _, lvl := range st.Levels() {
		if in.Settings.StructHeader {
			_ = in.U8() // version
			sub := in.Substream()
			if lvl.Load != nil {
				lvl.Load(sub)
			}
		} else if lvl.Load != nil {
			lvl.Load(in)
		}
	}
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// ResultErrorMask is set in the high bit of a result code to mark an error.
const ResultErrorMask uint32 = 1 << 31

// Result is a NEX result code.
type Result struct{ Code uint32 }

// SuccessResult clears the error bit.
func SuccessResult(code uint32) Result { return Result{code &^ ResultErrorMask} }

// ErrorResult sets the error bit.
func ErrorResult(code uint32) Result { return Result{code | ResultErrorMask} }

// IsSuccess reports whether the error bit is clear.
func (r Result) IsSuccess() bool { return r.Code&ResultErrorMask == 0 }

// IsError reports whether the error bit is set.
func (r Result) IsError() bool { return r.Code&ResultErrorMask != 0 }

// ---------------------------------------------------------------------------
// DateTime (packed bitfield, seconds..year)
// ---------------------------------------------------------------------------

// DateTime is a NEX timestamp packed into a u64 bitfield.
type DateTime uint64

// Value returns the raw packed value.
func (d DateTime) Value() uint64 { return uint64(d) }

// Second/Minute/Hour/Day/Month/Year unpack the fields.
func (d DateTime) Second() int { return int(d & 63) }
func (d DateTime) Minute() int { return int((d >> 6) & 63) }
func (d DateTime) Hour() int   { return int((d >> 12) & 31) }
func (d DateTime) Day() int    { return int((d >> 17) & 31) }
func (d DateTime) Month() int  { return int((d >> 22) & 15) }
func (d DateTime) Year() int   { return int(d >> 26) }

// MakeDateTime packs a calendar date/time into a DateTime.
func MakeDateTime(year, month, day, hour, minute, second int) DateTime {
	return DateTime(uint64(second) | uint64(minute)<<6 | uint64(hour)<<12 |
		uint64(day)<<17 | uint64(month)<<22 | uint64(year)<<26)
}

// NowDateTime returns the current UTC time as a NEX DateTime.
func NowDateTime() DateTime {
	t := time.Now().UTC()
	return MakeDateTime(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// ---------------------------------------------------------------------------
// StationURL
// ---------------------------------------------------------------------------

// StationURL is a NEX station address of the form "scheme:/k=v;k=v". Parameter
// insertion order is preserved so the serialized form round-trips a capture.
type StationURL struct {
	Scheme string
	keys   []string
	params map[string]string
}

// NewStationURL returns an empty station URL with the given scheme (default
// "prudp" when empty).
func NewStationURL(scheme string) *StationURL {
	if scheme == "" {
		scheme = "prudp"
	}
	return &StationURL{Scheme: scheme, params: map[string]string{}}
}

// ParseStationURL parses "scheme:/k=v;k=v"; an empty string yields an empty URL.
func ParseStationURL(s string) *StationURL {
	u := &StationURL{params: map[string]string{}}
	if s == "" {
		return u
	}
	i := strings.Index(s, ":/")
	if i < 0 {
		return u
	}
	u.Scheme = s[:i]
	fields := s[i+2:]
	if fields != "" {
		for _, f := range strings.Split(fields, ";") {
			kv := strings.SplitN(f, "=", 2)
			if len(kv) == 2 {
				u.Set(kv[0], kv[1])
			}
		}
	}
	return u
}

// Set assigns a parameter, preserving first-seen key order.
func (u *StationURL) Set(k, v string) {
	if u.params == nil {
		u.params = map[string]string{}
	}
	if _, ok := u.params[k]; !ok {
		u.keys = append(u.keys, k)
	}
	u.params[k] = v
}

// SetInt assigns an integer parameter.
func (u *StationURL) SetInt(k string, v int) { u.Set(k, strconv.Itoa(v)) }

// Get returns a string parameter (empty if unset).
func (u *StationURL) Get(k string) string { return u.params[k] }

// GetInt returns an integer parameter (0 if unset or non-numeric).
func (u *StationURL) GetInt(k string) int { n, _ := strconv.Atoi(u.params[k]); return n }

// Has reports whether a parameter is present.
func (u *StationURL) Has(k string) bool { _, ok := u.params[k]; return ok }

// Remove drops a parameter, from the map AND from the key order — leaving it in keys
// would re-emit it as "k=" on the wire, which is not the same as absent: the console
// distinguishes a missing "type" from an empty one when picking a LAN candidate.
func (u *StationURL) Remove(k string) {
	if _, ok := u.params[k]; !ok {
		return
	}
	delete(u.params, k)
	for i, kk := range u.keys {
		if kk == k {
			u.keys = append(u.keys[:i], u.keys[i+1:]...)
			break
		}
	}
}

// Copy returns a deep copy of the station URL (independent keys + params).
func (u *StationURL) Copy() *StationURL {
	c := &StationURL{Scheme: u.Scheme, keys: append([]string(nil), u.keys...), params: map[string]string{}}
	for k, v := range u.params {
		c.params[k] = v
	}
	return c
}

// String renders the URL as "scheme:/k=v;k=v".
func (u *StationURL) String() string {
	parts := make([]string, 0, len(u.keys))
	for _, k := range u.keys {
		parts = append(parts, k+"="+u.params[k])
	}
	joined := strings.Join(parts, ";")
	if u.Scheme != "" {
		return u.Scheme + ":/" + joined
	}
	return joined
}

// ---------------------------------------------------------------------------
// ResultRange (offset/size window used by list queries)
// ---------------------------------------------------------------------------

// ResultRange is a NEX pagination window.
type ResultRange struct {
	Offset uint32
	Size   uint32
}

// Levels implements Structure.
func (r *ResultRange) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.U32(r.Offset); o.U32(r.Size) },
		Load: func(i *StreamIn) { r.Offset = i.U32(); r.Size = i.U32() },
	}}
}

// ---------------------------------------------------------------------------
// Variant (tagged value)
// ---------------------------------------------------------------------------

// Variant type tags.
const (
	VariantNil      uint8 = 0
	VariantInt64    uint8 = 1
	VariantDouble   uint8 = 2
	VariantBool     uint8 = 3
	VariantString   uint8 = 4
	VariantDateTime uint8 = 5
	VariantUint64   uint8 = 6
)

// Variant is a NEX Variant: a value tagged with its type.
type Variant struct {
	Type   uint8
	Int    int64
	Double float64
	Bool   bool
	String string
	Date   DateTime
	Uint   uint64
}

// Variant writes a tagged value.
func (s *StreamOut) Variant(v Variant) {
	s.U8(v.Type)
	switch v.Type {
	case VariantInt64:
		s.S64(v.Int)
	case VariantDouble:
		s.Double(v.Double)
	case VariantBool:
		s.Bool(v.Bool)
	case VariantString:
		s.String(v.String)
	case VariantDateTime:
		s.DateTime(v.Date.Value())
	case VariantUint64:
		s.U64(v.Uint)
	}
}

// Variant reads a tagged value.
func (s *StreamIn) Variant() Variant {
	v := Variant{Type: s.U8()}
	switch v.Type {
	case VariantInt64:
		v.Int = s.S64()
	case VariantDouble:
		v.Double = s.Double()
	case VariantBool:
		v.Bool = s.Bool()
	case VariantString:
		v.String = s.String()
	case VariantDateTime:
		v.Date = DateTime(s.DateTime())
	case VariantUint64:
		v.Uint = s.U64()
	}
	return v
}

// ---------------------------------------------------------------------------
// DataHolder ("any data": a named structure)
// ---------------------------------------------------------------------------

// DataHolder wraps a named Structure. On the wire: the class name (String), a
// u32 equal to the following buffer's total size, then a Buffer whose contents
// are themselves a length-prefixed structure body.
type DataHolder struct {
	Name string
	Data Structure
}

// AnyData writes a DataHolder.
func (s *StreamOut) AnyData(h DataHolder) {
	s.String(h.Name)
	sub := NewStreamOut(s.Settings)
	if h.Data != nil {
		sub.Add(h.Data)
	}
	body := sub.Bytes()
	s.U32(uint32(len(body) + 4))
	s.Buffer(body)
}
