package nex

// NEX result codes (subset used by this stack). A code with the high bit set is
// an error; the same numeric value with the bit clear is its success flavor
// (e.g. login success is returned as ResultCoreUnknown without the error bit).
const (
	ResultCoreUnknown                uint32 = 0x00010001
	ResultCoreNotImplemented         uint32 = 0x00010002
	ResultCoreAccessDenied           uint32 = 0x00010006
	ResultCoreInvalidArgument        uint32 = 0x0001000A
	ResultRendezVousInvalidPID       uint32 = 0x0003006B
	ResultRendezVousSessionVoid      uint32 = 0x00030073
	ResultRendezVousSessionFull      uint32 = 0x000300C8
	ResultRendezVousPermissionDenied uint32 = 0x000300D9
	ResultAuthTokenParseError        uint32 = 0x00680002
	ResultAuthUnknown                uint32 = 0x0068000E
)
