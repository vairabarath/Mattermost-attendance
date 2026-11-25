// Package attendance holds the attendance business logic: the single
// net-hours calculation used by every report, and the slash-command service.
package attendance

import (
	"fmt"
	"sort"
	"time"
)

// Break is a pause inside a session. End is nil while the break is open.
type Break struct {
	Start time.Time
	End   *time.Time
}

// Session is one check-in/check-out span in one team. CheckOut is nil while
// the session is open.
type Session struct {
	ID       int64
	TeamID   string
	CheckIn  time.Time
	CheckOut *time.Time
	WorkMode string
	Breaks   []Break

	// Filled by queries that need them (reports, export).
	AutoClosed bool
	InNote     string
	OutSummary string
}

// DayKey identifies one member's work in one team on one local calendar day.
// Day is formatted as YYYY-MM-DD in the configured timezone (IST).
type DayKey struct {
	Day    string
	TeamID string
}

// DayFormat is the layout used for DayKey.Day.
const DayFormat = "2006-01-02"

type interval struct{ start, end time.Time }

// NetByDay is THE hours calculation. Every report must use it so numbers
// always agree.
//
// For each session it takes [check_in, check_out) (or [check_in, now) when
// open), subtracts its breaks (an open break runs until the session end),
// then splits what remains at local midnights in loc, so a session that runs
// past midnight is credited to both days.
func NetByDay(sessions []Session, now time.Time, loc *time.Location) map[DayKey]time.Duration {
	out := make(map[DayKey]time.Duration)
	for _, s := range sessions {
		for _, iv := range workIntervals(s, now) {
			for _, part := range splitAtMidnight(iv, loc) {
				key := DayKey{Day: part.start.In(loc).Format(DayFormat), TeamID: s.TeamID}
				out[key] += part.end.Sub(part.start)
			}
		}
	}
	return out
}

// workIntervals returns the session span minus its breaks, as sorted,
// non-overlapping intervals.
func workIntervals(s Session, now time.Time) []interval {
	end := now
	if s.CheckOut != nil {
		end = *s.CheckOut
	}
	if !end.After(s.CheckIn) {
		return nil
	}

	// Clamp each break to the session span; open breaks run to the end.
	var breaks []interval
	for _, b := range s.Breaks {
		bEnd := end
		if b.End != nil && b.End.Before(end) {
			bEnd = *b.End
		}
		bStart := b.Start
		if bStart.Before(s.CheckIn) {
			bStart = s.CheckIn
		}
		if bEnd.After(bStart) {
			breaks = append(breaks, interval{bStart, bEnd})
		}
	}
	sort.Slice(breaks, func(i, j int) bool { return breaks[i].start.Before(breaks[j].start) })

	// Walk the span, emitting the gaps between (possibly overlapping) breaks.
	var res []interval
	cursor := s.CheckIn
	for _, b := range breaks {
		if b.start.After(cursor) {
			res = append(res, interval{cursor, b.start})
		}
		if b.end.After(cursor) {
			cursor = b.end
		}
	}
	if end.After(cursor) {
		res = append(res, interval{cursor, end})
	}
	return res
}

// splitAtMidnight cuts an interval at every local midnight it crosses.
// time.Date normalises day+1 across month/year ends and handles DST zones.
func splitAtMidnight(iv interval, loc *time.Location) []interval {
	var parts []interval
	cursor := iv.start
	for cursor.Before(iv.end) {
		y, m, d := cursor.In(loc).Date()
		nextMidnight := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
		segEnd := iv.end
		if nextMidnight.Before(segEnd) {
			segEnd = nextMidnight
		}
		parts = append(parts, interval{cursor, segEnd})
		cursor = segEnd
	}
	return parts
}

// DayBounds returns the UTC instants [start, end) of the local calendar day
// containing t.
func DayBounds(t time.Time, loc *time.Location) (time.Time, time.Time) {
	y, m, d := t.In(loc).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, loc)
	return start, time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// TeamTotalsForDay sums NetByDay results for one day, per team.
func TeamTotalsForDay(totals map[DayKey]time.Duration, day string) map[string]time.Duration {
	res := make(map[string]time.Duration)
	for k, v := range totals {
		if k.Day == day {
			res[k.TeamID] += v
		}
	}
	return res
}

// FormatDuration renders a duration as "7h 45m" (truncated to the minute).
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d / time.Minute)
	return fmt.Sprintf("%dh %dm", mins/60, mins%60)
}
