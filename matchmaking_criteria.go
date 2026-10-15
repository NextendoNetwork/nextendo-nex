package nex

import (
	"strconv"
	"strings"
)

// criteriaAccept reports whether a gathering satisfies at least one of the search criteria
// sent with AutoMatchmake. An empty list accepts everything. Filters follow NEX: "" is any
// value, "N" an exact value, "min,max" an inclusive range.
func criteriaAccept(criteria []MatchmakeSessionSearchCriteria, s *MatchmakeSession, members int) bool {
	if len(criteria) == 0 {
		return true
	}
	for i := range criteria {
		if criterionAccepts(&criteria[i], s, members) {
			return true
		}
	}
	return false
}

func criterionAccepts(c *MatchmakeSessionSearchCriteria, s *MatchmakeSession, members int) bool {
	if !filterAccepts(c.GameMode, uint64(s.GameMode)) ||
		!filterAccepts(c.MinParticipants, uint64(s.MinParticipants)) ||
		!filterAccepts(c.MaxParticipants, uint64(s.MaxParticipants)) ||
		!filterAccepts(c.MatchmakeSystem, uint64(s.MatchmakeSystem)) {
		return false
	}
	for i, f := range c.Attribs {
		if strings.TrimSpace(f) == "" {
			continue
		}
		if i >= len(s.Attribs) || !filterAccepts(f, uint64(s.Attribs[i])) {
			return false
		}
	}
	if c.VacantOnly {
		need := int(c.VacantParticipants)
		if need < 1 {
			need = 1
		}
		if int(s.MaxParticipants)-members < need {
			return false
		}
	}
	if c.ExcludeLocked && !s.OpenParticipation {
		return false
	}
	if c.ExcludeUserPassword && s.UserPasswordEnabled {
		return false
	}
	if c.ExcludeSysPassword && s.SystemPasswordEnabled {
		return false
	}
	if c.Codeword != "" && c.Codeword != s.Codeword {
		return false
	}
	return true
}

// filterAccepts applies one NEX search filter string to a value; an unparsable filter accepts.
func filterAccepts(filter string, v uint64) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	lo, hi, isRange := strings.Cut(filter, ",")
	min, err := strconv.ParseUint(strings.TrimSpace(lo), 10, 64)
	if err != nil {
		return true
	}
	if !isRange {
		return v == min
	}
	max, err := strconv.ParseUint(strings.TrimSpace(hi), 10, 64)
	if err != nil {
		return v >= min
	}
	return v >= min && v <= max
}
