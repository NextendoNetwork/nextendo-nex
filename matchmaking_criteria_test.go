package nex

import "testing"

func TestCriteriaAccept(t *testing.T) {
	s := &MatchmakeSession{GameMode: 0, Attribs: []uint32{12134, 1, 17000, 0, 0, 0}, OpenParticipation: true}
	s.MaxParticipants = 8

	cases := []struct {
		name     string
		criteria []MatchmakeSessionSearchCriteria
		want     bool
	}{
		{"none", nil, true},
		{"exact attrib", []MatchmakeSessionSearchCriteria{{Attribs: []string{"12134", "1"}}}, true},
		{"wrong attrib", []MatchmakeSessionSearchCriteria{{Attribs: []string{"12134", "0"}}}, false},
		{"range", []MatchmakeSessionSearchCriteria{{Attribs: []string{"", "", "16000,18000"}}}, true},
		{"out of range", []MatchmakeSessionSearchCriteria{{Attribs: []string{"", "", "1,10"}}}, false},
		{"any of", []MatchmakeSessionSearchCriteria{{Attribs: []string{"", "0"}}, {Attribs: []string{"", "1"}}}, true},
		{"game mode", []MatchmakeSessionSearchCriteria{{GameMode: "3"}}, false},
		{"vacant", []MatchmakeSessionSearchCriteria{{VacantOnly: true, VacantParticipants: 9}}, false},
	}
	for _, c := range cases {
		if got := criteriaAccept(c.criteria, s, 1); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
