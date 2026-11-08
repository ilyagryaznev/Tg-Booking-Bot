// Package scheduling contains pure, side-effect-free slot calculation logic.
package scheduling

import (
	"time"

	"github.com/yourname/tg-booking-bot/internal/domain"
)

// Hours describes a working day.
type Hours struct {
	Start, End int           // hours of day in the business time zone
	Step       time.Duration // grid step for possible start times
	Location   *time.Location
}

// DayBounds returns the working interval for the calendar day of `day`.
func (h Hours) DayBounds(day time.Time) domain.Interval {
	d := day.In(h.Location)
	y, m, dd := d.Date()
	return domain.Interval{
		Start: time.Date(y, m, dd, h.Start, 0, 0, 0, h.Location),
		End:   time.Date(y, m, dd, h.End, 0, 0, 0, h.Location),
	}
}

// FreeSlots returns every start time on `day` at which a visit of length `dur`
// fits inside working hours, does not overlap any busy interval and starts
// after `now`. Busy intervals do not need to be sorted.
func FreeSlots(h Hours, day time.Time, dur time.Duration, busy []domain.Interval, now time.Time) []time.Time {
	bounds := h.DayBounds(day)
	var out []time.Time
	for start := bounds.Start; !start.Add(dur).After(bounds.End); start = start.Add(h.Step) {
		if !start.After(now) {
			continue
		}
		cand := domain.Interval{Start: start, End: start.Add(dur)}
		free := true
		for _, b := range busy {
			if cand.Overlaps(b) {
				free = false
				break
			}
		}
		if free {
			out = append(out, start)
		}
	}
	return out
}

// Days returns `n` consecutive calendar days starting from today (in loc).
func Days(now time.Time, n int, loc *time.Location) []time.Time {
	t := now.In(loc)
	y, m, d := t.Date()
	first := time.Date(y, m, d, 0, 0, 0, 0, loc)
	out := make([]time.Time, n)
	for i := range out {
		out[i] = first.AddDate(0, 0, i)
	}
	return out
}
