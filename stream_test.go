package nex

import (
	"bytes"
	"testing"
)

func testSettings() *Settings { return NewSwitchSettings("09c1c475", 40000) }

// TestStringEncoding checks the NEX String layout: u16 length INCLUDING the null
// terminator, then the UTF-8 bytes and a trailing 0x00.
func TestStringEncoding(t *testing.T) {
	out := NewStreamOut(testSettings())
	out.String("test")
	want := []byte{0x05, 0x00, 't', 'e', 's', 't', 0x00}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("String: got % x, want % x", out.Bytes(), want)
	}
	// Round-trip.
	in := NewStreamIn(out.Bytes(), testSettings())
	if got := in.String(); got != "test" {
		t.Fatalf("String round-trip: got %q", got)
	}
}

// TestPID8 checks that an 8-byte PID is written little-endian.
func TestPID8(t *testing.T) {
	out := NewStreamOut(testSettings())
	out.PID(0x123456789A)
	want := []byte{0x9A, 0x78, 0x56, 0x34, 0x12, 0x00, 0x00, 0x00}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("PID: got % x, want % x", out.Bytes(), want)
	}
	if got := NewStreamIn(out.Bytes(), testSettings()).PID(); got != 0x123456789A {
		t.Fatalf("PID round-trip: got %#x", got)
	}
}

// TestStructHeaderLayout verifies the hierarchical struct-header framing that
// makes inherited structures byte-exact: [u8 version][u32 bodyLen][body].
func TestStructHeaderLayout(t *testing.T) {
	out := NewStreamOut(testSettings())
	out.Add(&ResultRange{Offset: 5, Size: 10})
	want := []byte{
		0x00,                   // version
		0x08, 0x00, 0x00, 0x00, // body length = 8
		0x05, 0x00, 0x00, 0x00, // offset = 5
		0x0A, 0x00, 0x00, 0x00, // size = 10
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("struct header: got % x, want % x", out.Bytes(), want)
	}
	// Round-trip.
	var rr ResultRange
	NewStreamIn(out.Bytes(), testSettings()).Extract(&rr)
	if rr.Offset != 5 || rr.Size != 10 {
		t.Fatalf("ResultRange round-trip: got %+v", rr)
	}
}

// TestStructHeaderDisabled verifies that with struct headers off (older NEX)
// the body is written flat, with no version/length framing.
func TestStructHeaderDisabled(t *testing.T) {
	s := testSettings()
	s.StructHeader = false
	out := NewStreamOut(s)
	out.Add(&ResultRange{Offset: 1, Size: 2})
	want := []byte{0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("flat struct: got % x, want % x", out.Bytes(), want)
	}
}

// TestStationURLRoundTrip checks parse/render and ordered parameters.
func TestStationURLRoundTrip(t *testing.T) {
	raw := "prudp:/address=1.2.3.4;port=60003;PID=2;RVCID=42;type=3"
	u := ParseStationURL(raw)
	if u.Scheme != "prudp" || u.Get("address") != "1.2.3.4" || u.GetInt("port") != 60003 || u.GetInt("RVCID") != 42 {
		t.Fatalf("StationURL parse: %+v", u)
	}
	if u.String() != raw {
		t.Fatalf("StationURL render: got %q want %q", u.String(), raw)
	}
}

// TestVariantRoundTrip exercises each Variant tag.
func TestVariantRoundTrip(t *testing.T) {
	cases := []Variant{
		{Type: VariantNil},
		{Type: VariantInt64, Int: -5},
		{Type: VariantUint64, Uint: 100},
		{Type: VariantBool, Bool: true},
		{Type: VariantString, String: "hi"},
		{Type: VariantDouble, Double: 1.5},
	}
	for _, c := range cases {
		out := NewStreamOut(testSettings())
		out.Variant(c)
		got := NewStreamIn(out.Bytes(), testSettings()).Variant()
		if got != c {
			t.Fatalf("Variant %d: got %+v want %+v", c.Type, got, c)
		}
	}
}

// TestListRoundTrip checks List<u32>.
func TestListRoundTrip(t *testing.T) {
	out := NewStreamOut(testSettings())
	WriteList(out, []uint32{7, 8, 9}, func(o *StreamOut, v uint32) { o.U32(v) })
	if out.Bytes()[0] != 3 {
		t.Fatalf("list count byte: %d", out.Bytes()[0])
	}
	got := ReadList(NewStreamIn(out.Bytes(), testSettings()), func(i *StreamIn) uint32 { return i.U32() })
	if len(got) != 3 || got[0] != 7 || got[2] != 9 {
		t.Fatalf("list round-trip: %v", got)
	}
}
