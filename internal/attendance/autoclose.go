package attendance

import "time"

// AutoCloseTime returns when a forgotten session should be closed so its
// net time equals expected: check_in + expected + break time taken before
// that point. It walks the session's work intervals (breaks removed, an open
// session/break running until now) and stops once `expected` of work has
// been counted. It never returns a time after now.
func AutoCloseTime(s Session, expected time.Duration, now time.Time) time.Time {
	remaining := expected
	for _, iv := range workIntervals(s, now) {
		d := iv.end.Sub(iv.start)
		if d >= remaining {
			return iv.start.Add(remaining)
		}
		remaining -= d
	}
	return now // less work than expected in total: cap at now
}
