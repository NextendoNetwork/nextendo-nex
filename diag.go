package nex

import "time"

// ts returns a wall-clock timestamp for server-side diagnostic lines. The nncs
// responder and the emulator both timestamp their output; the NEX logs did not,
// which made it impossible to order events (probes vs Register vs error) across
// processes. Prefix the few events that mark a session's timeline.
func ts() string {
	return time.Now().Format("15:04:05.000")
}
