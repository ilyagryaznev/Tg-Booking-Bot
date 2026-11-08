//go:build integration

// Run with: TEST_DATABASE_URL=postgres://... go test -tags integration ./...
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yourname/tg-booking-bot/internal/domain"
	"github.com/yourname/tg-booking-bot/migrations"
)

func setup(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	raw, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	// Running migrations twice must be a no-op.
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	return s
}

func booking(user int64, start time.Time, dur time.Duration) domain.Booking {
	return domain.Booking{UserID: user, ChatID: user, CustomerName: "Test", ServiceID: 1,
		StartsAt: start, EndsAt: start.Add(dur)}
}

func TestOverlapIsRejectedByDatabase(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)

	if _, err := s.CreateBooking(ctx, booking(1, start, time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBooking(ctx, booking(2, start.Add(30*time.Minute), time.Hour)); !errors.Is(err, domain.ErrSlotTaken) {
		t.Fatalf("overlap: want ErrSlotTaken, got %v", err)
	}
	// Back-to-back is fine.
	if _, err := s.CreateBooking(ctx, booking(2, start.Add(time.Hour), time.Hour)); err != nil {
		t.Fatalf("adjacent booking: %v", err)
	}
}

func TestConcurrentBookingsOnlyOneWins(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)

	const n = 20
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(u int64) {
			defer wg.Done()
			_, err := s.CreateBooking(ctx, booking(u, start, time.Hour))
			results <- err
		}(int64(i + 1))
	}
	wg.Wait()
	close(results)

	var ok, taken int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrSlotTaken):
			taken++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || taken != n-1 {
		t.Fatalf("ok=%d taken=%d, want 1 and %d", ok, taken, n-1)
	}
}

func TestCancelFreesSlotAndChecksOwner(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	start := time.Now().Add(24 * time.Hour).Truncate(time.Hour)

	id, err := s.CreateBooking(ctx, booking(1, start, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(ctx, id, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign cancel: want ErrNotFound, got %v", err)
	}
	b, err := s.Cancel(ctx, id, 1)
	if err != nil || b.ServiceName != "Haircut" {
		t.Fatalf("own cancel: %+v %v", b, err)
	}
	if _, err := s.CreateBooking(ctx, booking(3, start, time.Hour)); err != nil {
		t.Fatalf("slot should be free after cancel: %v", err)
	}
}

func TestClaimDueRemindersOnce(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	now := time.Now()

	soon, _ := s.CreateBooking(ctx, booking(1, now.Add(time.Hour), 30*time.Minute))
	s.CreateBooking(ctx, booking(2, now.Add(5*time.Hour), 30*time.Minute))

	got, err := s.ClaimDueReminders(ctx, now, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != soon {
		t.Fatalf("first claim = %+v", got)
	}
	again, err := s.ClaimDueReminders(ctx, now, 2*time.Hour)
	if err != nil || len(again) != 0 {
		t.Fatalf("reminder must be claimed only once, got %d (%v)", len(again), err)
	}
}

func TestBusyAndListing(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	day := time.Now().Add(96 * time.Hour).Truncate(24 * time.Hour)

	s.CreateBooking(ctx, booking(1, day.Add(10*time.Hour), time.Hour))
	s.CreateBooking(ctx, booking(1, day.Add(12*time.Hour), time.Hour))

	busy, err := s.BusyIntervals(ctx, day, day.Add(24*time.Hour))
	if err != nil || len(busy) != 2 {
		t.Fatalf("busy = %v, %v", busy, err)
	}
	mine, err := s.UpcomingForUser(ctx, 1, time.Now())
	if err != nil || len(mine) != 2 || !mine[0].StartsAt.Before(mine[1].StartsAt) {
		t.Fatalf("upcoming = %+v, %v", mine, err)
	}
	all, err := s.ActiveBetween(ctx, day, day.Add(11*time.Hour))
	if err != nil || len(all) != 1 {
		t.Fatalf("between = %+v, %v", all, err)
	}
	svcs, err := s.ListServices(ctx)
	if err != nil || len(svcs) != 4 {
		t.Fatalf("services = %v, %v", svcs, err)
	}
}
