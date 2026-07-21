package nex

// NEX matchmaking structures (the NEX 4.0.0 / Switch layout). These are the
// types MK8 exchanges during matchmaking; MatchmakeSession in particular must be
// byte-exact or the game rejects the session. The hierarchical struct-header
// framing (Gathering as the base level, MatchmakeSession as the derived level)
// is handled by the Structure machinery in types.go.

// Gathering is the base matchmaking object.
type Gathering struct {
	ID                  uint32
	OwnerPID            uint64
	HostPID             uint64
	MinParticipants     uint16
	MaxParticipants     uint16
	ParticipationPolicy uint32
	PolicyArgument      uint32
	Flags               uint32
	State               uint32
	Description         string
}

func (g *Gathering) gatheringLevel() Level {
	return Level{
		Save: func(o *StreamOut) {
			o.U32(g.ID)
			o.PID(g.OwnerPID)
			o.PID(g.HostPID)
			o.U16(g.MinParticipants)
			o.U16(g.MaxParticipants)
			o.U32(g.ParticipationPolicy)
			o.U32(g.PolicyArgument)
			o.U32(g.Flags)
			o.U32(g.State)
			o.String(g.Description)
		},
		Load: func(i *StreamIn) {
			g.ID = i.U32()
			g.OwnerPID = i.PID()
			g.HostPID = i.PID()
			g.MinParticipants = i.U16()
			g.MaxParticipants = i.U16()
			g.ParticipationPolicy = i.U32()
			g.PolicyArgument = i.U32()
			g.Flags = i.U32()
			g.State = i.U32()
			g.Description = i.String()
		},
	}
}

// Levels implements Structure.
func (g *Gathering) Levels() []Level { return []Level{g.gatheringLevel()} }

// MatchmakeParam is a map of string keys to Variant values.
type MatchmakeParam struct {
	Params map[string]Variant
}

// Levels implements Structure.
func (p *MatchmakeParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) {
			WriteMap(o, p.Params,
				func(o *StreamOut, k string) { o.String(k) },
				func(o *StreamOut, v Variant) { o.Variant(v) })
		},
		Load: func(i *StreamIn) {
			p.Params = ReadMap(i,
				func(i *StreamIn) string { return i.String() },
				func(i *StreamIn) Variant { return i.Variant() })
		},
	}}
}

// MatchmakeBlockListParam carries block-list options.
type MatchmakeBlockListParam struct {
	Options uint32
}

// Levels implements Structure.
func (p *MatchmakeBlockListParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.U32(p.Options) },
		Load: func(i *StreamIn) { p.Options = i.U32() },
	}}
}

// MatchmakeSession extends Gathering with the session-specific fields (NEX 4).
type MatchmakeSession struct {
	Gathering
	GameMode              uint32
	Attribs               []uint32
	OpenParticipation     bool
	MatchmakeSystem       uint32
	ApplicationData       []byte
	NumParticipants       uint32
	ProgressScore         uint8
	SessionKey            []byte
	Option                uint32
	Param                 MatchmakeParam
	StartedTime           DateTime
	UserPassword          string
	ReferGID              uint32
	UserPasswordEnabled   bool
	SystemPasswordEnabled bool
	Codeword              string
}

// Levels implements Structure: the inherited Gathering level followed by the
// MatchmakeSession level (NEX 4.0.0 field order).
func (m *MatchmakeSession) Levels() []Level {
	return []Level{
		m.gatheringLevel(),
		{
			Save: func(o *StreamOut) {
				o.U32(m.GameMode)
				WriteList(o, m.Attribs, func(o *StreamOut, v uint32) { o.U32(v) })
				o.Bool(m.OpenParticipation)
				o.U32(m.MatchmakeSystem)
				o.Buffer(m.ApplicationData)
				o.U32(m.NumParticipants)
				o.U8(m.ProgressScore)
				o.Buffer(m.SessionKey)
				o.U32(m.Option)
				o.Add(&m.Param)
				o.DateTime(m.StartedTime.Value())
				o.String(m.UserPassword)
				o.U32(m.ReferGID)
				o.Bool(m.UserPasswordEnabled)
				o.Bool(m.SystemPasswordEnabled)
				o.String(m.Codeword)
			},
			Load: func(i *StreamIn) {
				m.GameMode = i.U32()
				m.Attribs = ReadList(i, func(i *StreamIn) uint32 { return i.U32() })
				m.OpenParticipation = i.Bool()
				m.MatchmakeSystem = i.U32()
				m.ApplicationData = i.Buffer()
				m.NumParticipants = i.U32()
				m.ProgressScore = i.U8()
				m.SessionKey = i.Buffer()
				m.Option = i.U32()
				i.Extract(&m.Param)
				m.StartedTime = DateTime(i.DateTime())
				m.UserPassword = i.String()
				m.ReferGID = i.U32()
				m.UserPasswordEnabled = i.Bool()
				m.SystemPasswordEnabled = i.Bool()
				m.Codeword = i.String()
			},
		},
	}
}

// AutoMatchmakeParam is the parameter MK8 sends to auto-matchmake. Only the
// session (the first field) is decoded — it carries the game mode and attributes
// the matchmaker needs; the trailing fields (participants, search criteria,
// block list) are present on the wire but left unread, which is safe because the
// struct header bounds this object's body.
type AutoMatchmakeParam struct {
	Session MatchmakeSession
}

// Levels implements Structure.
func (a *AutoMatchmakeParam) Levels() []Level {
	return []Level{{
		Save: func(o *StreamOut) { o.Add(&a.Session) },
		Load: func(i *StreamIn) { i.Extract(&a.Session) },
	}}
}
