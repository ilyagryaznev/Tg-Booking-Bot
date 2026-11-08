// Package postgres implements persistence on top of PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/lib/pq"

	"github.com/yourname/tg-booking-bot/internal/domain"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Postgres may still be starting when the container comes up.
	var pingErr error
	for i := 0; i < 10; i++ {
		if pingErr = db.PingContext(ctx); pingErr == nil {
			return &Store{db: db}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	db.Close()
	return nil, fmt.Errorf("postgres is unreachable: %w", pingErr)
}

func (s *Store) Close() error { return s.db.Close() }

// Migrate applies every *.sql file from fsys that has not been applied yet,
// in lexical order, each in its own transaction. An advisory lock makes it
// safe to start several bot replicas at once.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	const lockID = 727274 // arbitrary app-wide constant
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}

	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, name := range files {
		var exists bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListServices(ctx context.Context) ([]domain.Service, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, duration_min, price_cents FROM services WHERE is_active ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		var sv domain.Service
		if err := rows.Scan(&sv.ID, &sv.Name, &sv.DurationMin, &sv.PriceCents); err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) GetService(ctx context.Context, id int) (domain.Service, error) {
	var sv domain.Service
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, duration_min, price_cents FROM services WHERE id = $1 AND is_active`, id).
		Scan(&sv.ID, &sv.Name, &sv.DurationMin, &sv.PriceCents)
	if errors.Is(err, sql.ErrNoRows) {
		return sv, domain.ErrNotFound
	}
	return sv, err
}

// BusyIntervals returns active bookings that intersect [from, to).
func (s *Store) BusyIntervals(ctx context.Context, from, to time.Time) ([]domain.Interval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT starts_at, ends_at FROM bookings
		WHERE status = 'active' AND starts_at < $2 AND ends_at > $1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Interval
	for rows.Next() {
		var iv domain.Interval
		if err := rows.Scan(&iv.Start, &iv.End); err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// CreateBooking inserts a booking. If the slot overlaps an existing active
// booking the exclusion constraint fires and ErrSlotTaken is returned.
func (s *Store) CreateBooking(ctx context.Context, b domain.Booking) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO bookings (user_id, chat_id, customer_name, service_id, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		b.UserID, b.ChatID, b.CustomerName, b.ServiceID, b.StartsAt, b.EndsAt).Scan(&id)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23P01" { // exclusion_violation
		return 0, domain.ErrSlotTaken
	}
	return id, err
}

const bookingCols = `b.id, b.user_id, b.chat_id, b.customer_name, b.service_id, s.name,
	b.starts_at, b.ends_at, b.status`

func scanBookings(rows *sql.Rows) ([]domain.Booking, error) {
	defer rows.Close()
	var out []domain.Booking
	for rows.Next() {
		var b domain.Booking
		if err := rows.Scan(&b.ID, &b.UserID, &b.ChatID, &b.CustomerName, &b.ServiceID,
			&b.ServiceName, &b.StartsAt, &b.EndsAt, &b.Status); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpcomingForUser lists a customer's active bookings that have not started yet.
func (s *Store) UpcomingForUser(ctx context.Context, userID int64, now time.Time) ([]domain.Booking, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bookingCols+`
		FROM bookings b JOIN services s ON s.id = b.service_id
		WHERE b.user_id = $1 AND b.status = 'active' AND b.starts_at > $2
		ORDER BY b.starts_at`, userID, now)
	if err != nil {
		return nil, err
	}
	return scanBookings(rows)
}

// ActiveBetween lists all active bookings starting in [from, to) — admin schedule view.
func (s *Store) ActiveBetween(ctx context.Context, from, to time.Time) ([]domain.Booking, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bookingCols+`
		FROM bookings b JOIN services s ON s.id = b.service_id
		WHERE b.status = 'active' AND b.starts_at >= $1 AND b.starts_at < $2
		ORDER BY b.starts_at`, from, to)
	if err != nil {
		return nil, err
	}
	return scanBookings(rows)
}

// Cancel marks a booking as cancelled. When userID is non-zero the booking
// must belong to that user (customers can only cancel their own visits).
func (s *Store) Cancel(ctx context.Context, id, userID int64) (domain.Booking, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH upd AS (
			UPDATE bookings SET status = 'cancelled'
			WHERE id = $1 AND status = 'active' AND ($2 = 0 OR user_id = $2)
			RETURNING *
		)
		SELECT `+bookingCols+` FROM upd b JOIN services s ON s.id = b.service_id`, id, userID)
	if err != nil {
		return domain.Booking{}, err
	}
	list, err := scanBookings(rows)
	if err != nil {
		return domain.Booking{}, err
	}
	if len(list) == 0 {
		return domain.Booking{}, domain.ErrNotFound
	}
	return list[0], nil
}

// ClaimDueReminders atomically marks and returns bookings that start within
// `lead` from now and have not been reminded yet. SKIP LOCKED lets several
// replicas run the reminder loop without sending duplicates.
func (s *Store) ClaimDueReminders(ctx context.Context, now time.Time, lead time.Duration) ([]domain.Booking, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH due AS (
			SELECT id FROM bookings
			WHERE status = 'active' AND NOT reminder_sent
			  AND starts_at > $1 AND starts_at <= $2
			FOR UPDATE SKIP LOCKED
		), upd AS (
			UPDATE bookings SET reminder_sent = TRUE
			WHERE id IN (SELECT id FROM due)
			RETURNING *
		)
		SELECT `+bookingCols+` FROM upd b JOIN services s ON s.id = b.service_id
		ORDER BY b.starts_at`, now, now.Add(lead))
	if err != nil {
		return nil, err
	}
	return scanBookings(rows)
}
