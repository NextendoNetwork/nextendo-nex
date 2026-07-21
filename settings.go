package nex

// Settings holds the version-dependent parameters that drive NEX/PRUDP wire
// encoding. The values are those Nintendo uses per platform; the Switch (NEX 4)
// profile below is taken from observed traffic and the public protocol docs.
//
// Switch profile in one line: PRUDP v2 ("Lite") over a WebSocket, NO PRUDP-layer
// encryption (the WSS/TLS tunnel provides confidentiality), 8-byte PIDs,
// structure headers enabled, and the "new" Kerberos key derivation.

// Transport kinds.
const (
	TransportUDP       = 0
	TransportTCP       = 1
	TransportWebSocket = 2
)

// Compression kinds.
const (
	CompressionNone = 0
	CompressionZlib = 1
)

// Encryption kinds (PRUDP payload layer).
const (
	EncryptionNone = 0
	EncryptionRC4  = 1
)

// Kerberos key-derivation kinds.
const (
	// KeyDerivationOld: md5(password) iterated (65000 + pid%1024) times.
	KeyDerivationOld = 0
	// KeyDerivationNew: md5(password) once, append pid as u64 LE, md5 once.
	KeyDerivationNew = 1
)

// Settings is the per-server wire configuration.
type Settings struct {
	// nex.*
	NexVersion    int  // e.g. 40000 for a 4.0.0 title
	ClientVersion int  // required for NEX >= 4.4.0
	StructHeader  bool // prefix each structure level with version + length
	PIDSize       int  // 4 or 8

	// prudp.*
	AccessKey          string
	PrudpVersion       int // 0 = v0, 1 = v1, 2 = "Lite"
	PrudpMinorVersion  int
	SupportedFunctions int
	Transport          int
	Compression        int
	Encryption         int
	ResendTimeout      float64
	ResendLimit        int
	PingTimeout        float64
	FragmentSize       int
	MaxSubstreamID     int

	// kerberos.*
	KerberosKeySize       int
	KerberosKeyDerivation int
	KerberosTicketVersion int
}

// NewSwitchSettings returns the Switch (NEX 4) wire profile for the given access
// key and NEX version (e.g. 40000 for Mario Kart 8 Deluxe, which reports 4.0.0).
func NewSwitchSettings(accessKey string, nexVersion int) *Settings {
	return &Settings{
		NexVersion:   nexVersion,
		StructHeader: true,
		PIDSize:      8,

		AccessKey:         accessKey,
		PrudpVersion:      2, // "Lite"
		PrudpMinorVersion: 5,
		Transport:         TransportWebSocket,
		Compression:       CompressionNone,
		Encryption:        EncryptionNone, // WSS/TLS handles confidentiality
		ResendTimeout:     5,
		ResendLimit:       0,
		PingTimeout:       5,
		FragmentSize:      1300,
		MaxSubstreamID:    0,

		KerberosKeySize:       32,
		KerberosKeyDerivation: KeyDerivationNew,
		KerberosTicketVersion: 1,
	}
}
