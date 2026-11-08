package scheduling

import (
	"testing"
	"time"

	"github.com/yourname/tg-booking-bot/internal/domain"
)

var loc = time.FixedZone("TEST", 3*3600)

func at(day, hh, mm int) time.Time { return time.Date(2026, 11, day, hh, mm, 0, 0, loc) }

func hours() Hours { return Hours{Start: 10, End: 14, Step: 30 * time.Minute, Location: loc} }

func fmtSlots(ts []time.Time) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Format("15:04")
	}
	return out
}

func TestFreeSlots(t *testing.T) {
	early := at(1, 0, 0) // midnight, so "now" filters nothing
	tests := []struct {
		name string
		dur  time.Duration
		busy []domain.Interval
		now  time.Time
		want []string
	}{
		{
			name: "empty day, 1h visits",
			dur:  time.Hour,
			now:  early,
			want: []string{"10:00", "10:30", "11:00", "11:30", "12:00", "12:30", "13:00"},
		},
		{
			name: "busy 11:00-12:00 blocks overlapping starts only",
			dur:  time.Hour,
			busy: []domain.Interval{{Start: at(2, 11, 0), End: at(2, 12, 0)}},
			now:  early,
			want: []string{"10:00", "12:00", "12:30", "13:00"},
		},
		{
			name: "adjacent bookings do not conflict (half-open intervals)",
			dur:  30 * time.Minute,
			busy: []domain.Interval{{Start: at(2, 10, 30), End: at(2, 13, 30)}},
			now:  early,
			want: []string{"10:00", "13:30"},
		},
		{
			name: "past slots are hidden",
			dur:  time.Hour,
			now:  at(2, 12, 10),
			want: []string{"12:30", "13:00"},
		},
		{
			name: "visit longer than the day",
			dur:  5 * time.Hour,
			now:  early,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fmtSlots(FreeSlots(hours(), at(2, 9, 0), tc.dur, tc.busy, tc.now))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDays(t *testing.T) {
	// 23:30 UTC on Nov 1 is already Nov 2 in UTC+3.
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	days := Days(now, 3, loc)
	if len(days) != 3 {
		t.Fatalf("want 3 days, got %d", len(days))
	}
	if got := days[0].Format(time.DateOnly); got != "2026-11-02" {
		t.Errorf("first day = %s, want 2026-11-02", got)
	}
	if got := days[2].Format(time.DateOnly); got != "2026-11-04" {
		t.Errorf("last day = %s, want 2026-11-04", got)
	}
}
