package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/yourname/tg-booking-bot/internal/domain"
	"github.com/yourname/tg-booking-bot/internal/scheduling"
	"github.com/yourname/tg-booking-bot/internal/storage/redisstore"
)

// ---------- fakes ----------

type fakeRepo struct {
	mu       sync.Mutex
	services []domain.Service
	bookings []domain.Booking
}

func (r *fakeRepo) ListServices(context.Context) ([]domain.Service, error) { return r.services, nil }

func (r *fakeRepo) GetService(_ context.Context, id int) (domain.Service, error) {
	for _, s := range r.services {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Service{}, domain.ErrNotFound
}

func (r *fakeRepo) BusyIntervals(_ context.Context, from, to time.Time) ([]domain.Interval, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Interval
	for _, b := range r.bookings {
		if b.Status == domain.StatusActive && b.StartsAt.Before(to) && b.EndsAt.After(from) {
			out = append(out, domain.Interval{Start: b.StartsAt, End: b.EndsAt})
		}
	}
	return out, nil
}

func (r *fakeRepo) CreateBooking(_ context.Context, b domain.Booking) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	iv := domain.Interval{Start: b.StartsAt, End: b.EndsAt}
	for _, x := range r.bookings {
		if x.Status == domain.StatusActive && iv.Overlaps(domain.Interval{Start: x.StartsAt, End: x.EndsAt}) {
			return 0, domain.ErrSlotTaken
		}
	}
	b.ID = int64(len(r.bookings) + 1)
	b.Status = domain.StatusActive
	r.bookings = append(r.bookings, b)
	return b.ID, nil
}

func (r *fakeRepo) UpcomingForUser(_ context.Context, userID int64, now time.Time) ([]domain.Booking, error) {
	var out []domain.Booking
	for _, b := range r.bookings {
		if b.UserID == userID && b.Status == domain.StatusActive && b.StartsAt.After(now) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (r *fakeRepo) ActiveBetween(_ context.Context, from, to time.Time) ([]domain.Booking, error) {
	var out []domain.Booking
	for _, b := range r.bookings {
		if b.Status == domain.StatusActive && !b.StartsAt.Before(from) && b.StartsAt.Before(to) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (r *fakeRepo) Cancel(_ context.Context, id, userID int64) (domain.Booking, error) {
	for i, b := range r.bookings {
		if b.ID == id && b.Status == domain.StatusActive && (userID == 0 || b.UserID == userID) {
			r.bookings[i].Status = domain.StatusCancelled
			return r.bookings[i], nil
		}
	}
	return domain.Booking{}, domain.ErrNotFound
}

func (r *fakeRepo) ClaimDueReminders(context.Context, time.Time, time.Duration) ([]domain.Booking, error) {
	return nil, nil
}

type fakeSessions struct {
	drafts  map[int64]redisstore.Draft
	blocked bool
}

func (s *fakeSessions) SaveDraft(_ context.Context, id int64, d redisstore.Draft) error {
	s.drafts[id] = d
	return nil
}
func (s *fakeSessions) GetDraft(_ context.Context, id int64) (redisstore.Draft, bool, error) {
	d, ok := s.drafts[id]
	return d, ok, nil
}
func (s *fakeSessions) DeleteDraft(_ context.Context, id int64) error {
	delete(s.drafts, id)
	return nil
}
func (s *fakeSessions) Allow(context.Context, int64) (bool, error) { return !s.blocked, nil }

type sent struct {
	chatID int64
	text   string
	kb     *tgbotapi.InlineKeyboardMarkup
}

type fakeSender struct{ out []sent }

func (f *fakeSender) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	switch m := c.(type) {
	case tgbotapi.MessageConfig:
		s := sent{chatID: m.ChatID, text: m.Text}
		if kb, ok := m.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup); ok {
			s.kb = &kb
		}
		f.out = append(f.out, s)
	case tgbotapi.EditMessageTextConfig:
		f.out = append(f.out, sent{chatID: m.ChatID, text: m.Text, kb: m.ReplyMarkup})
	}
	return tgbotapi.Message{}, nil
}

func (f *fakeSender) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (f *fakeSender) last() sent { return f.out[len(f.out)-1] }

// ---------- harness ----------

const (
	customer = int64(100)
	admin    = int64(1)
)

var loc = time.FixedZone("TEST", 3*3600)

type harness struct {
	t      *testing.T
	bot    *Bot
	repo   *fakeRepo
	sess   *fakeSessions
	sender *fakeSender
}

func newHarness(t *testing.T) *harness {
	repo := &fakeRepo{services: []domain.Service{
		{ID: 1, Name: "Haircut", DurationMin: 60, PriceCents: 3000},
		{ID: 2, Name: "Beard trim", DurationMin: 30, PriceCents: 1500},
	}}
	sess := &fakeSessions{drafts: map[int64]redisstore.Draft{}}
	sender := &fakeSender{}
	b := New(sender, repo, sess, Settings{
		BusinessName: "Test Shop",
		Hours:        scheduling.Hours{Start: 10, End: 12, Step: 30 * time.Minute, Location: loc},
		BookingDays:  3,
		Admins:       map[int64]bool{admin: true},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Now = func() time.Time { return time.Date(2026, 11, 2, 8, 0, 0, 0, loc) }
	return &harness{t: t, bot: b, repo: repo, sess: sess, sender: sender}
}

func (h *harness) click(userID int64, data string) {
	h.bot.Handle(context.Background(), tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "cb",
		From:    &tgbotapi.User{ID: userID, FirstName: "Alice"},
		Message: &tgbotapi.Message{MessageID: 7, Chat: &tgbotapi.Chat{ID: userID}},
		Data:    data,
	}})
}

func (h *harness) text(userID int64, text string) {
	h.bot.Handle(context.Background(), tgbotapi.Update{Message: &tgbotapi.Message{
		From: &tgbotapi.User{ID: userID, FirstName: "Alice"},
		Chat: &tgbotapi.Chat{ID: userID},
		Text: text,
	}})
}

func buttons(kb *tgbotapi.InlineKeyboardMarkup) []string {
	var out []string
	if kb == nil {
		return out
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, *b.CallbackData)
		}
	}
	return out
}

func slot(day, hh, mm int) string {
	return fmt.Sprintf("time:%d", time.Date(2026, 11, day, hh, mm, 0, 0, loc).Unix())
}

// ---------- tests ----------

func TestFullBookingFlow(t *testing.T) {
	h := newHarness(t)

	h.text(customer, btnBook)
	if got := buttons(h.sender.last().kb); len(got) != 2 || got[0] != "svc:1" {
		t.Fatalf("service buttons = %v", got)
	}

	h.click(customer, "svc:1")
	days := buttons(h.sender.last().kb)
	if days[0] != "day:2026-11-02" || days[len(days)-1] != "back:svc" {
		t.Fatalf("day buttons = %v", days)
	}

	h.click(customer, "day:2026-11-02")
	// 10:00-12:00 window, 1h visit, 30m step → 10:00, 10:30, 11:00
	times := buttons(h.sender.last().kb)
	want := []string{slot(2, 10, 0), slot(2, 10, 30), slot(2, 11, 0), "back:days"}
	if strings.Join(times, ",") != strings.Join(want, ",") {
		t.Fatalf("time buttons = %v, want %v", times, want)
	}

	h.click(customer, slot(2, 10, 30))
	if !strings.Contains(h.sender.last().text, "Confirm?") {
		t.Fatalf("expected confirmation, got %q", h.sender.last().text)
	}

	h.click(customer, "ok")
	if len(h.repo.bookings) != 1 {
		t.Fatalf("expected 1 booking, got %d", len(h.repo.bookings))
	}
	bk := h.repo.bookings[0]
	if bk.UserID != customer || bk.EndsAt.Sub(bk.StartsAt) != time.Hour {
		t.Fatalf("unexpected booking %+v", bk)
	}
	if _, ok := h.sess.drafts[customer]; ok {
		t.Error("draft must be deleted after booking")
	}

	var custOK, adminOK bool
	for _, s := range h.sender.out {
		custOK = custOK || (s.chatID == customer && strings.Contains(s.text, "You're booked"))
		adminOK = adminOK || (s.chatID == admin && strings.Contains(s.text, "New booking #1"))
	}
	if !custOK || !adminOK {
		t.Errorf("customer confirmed=%v, admin notified=%v", custOK, adminOK)
	}

	// The booked hour disappears from the grid for the next customer.
	h.click(200, "svc:1")
	h.click(200, "day:2026-11-02")
	if got := buttons(h.sender.last().kb); strings.Join(got, ",") != "back:days" {
		t.Fatalf("10:30-11:30 is booked, so no 1h slot fits; got %v", got)
	}
}

func TestSlotTakenWhileConfirming(t *testing.T) {
	h := newHarness(t)
	h.click(customer, "svc:2")
	h.click(customer, "day:2026-11-02")
	h.click(customer, slot(2, 11, 0))

	// Another customer books the same slot first.
	h.repo.CreateBooking(context.Background(), domain.Booking{
		UserID: 999, ServiceID: 2,
		StartsAt: time.Date(2026, 11, 2, 11, 0, 0, 0, loc),
		EndsAt:   time.Date(2026, 11, 2, 11, 30, 0, 0, loc),
	})

	h.click(customer, "ok")
	if len(h.repo.bookings) != 1 {
		t.Fatalf("double booking created")
	}
	var warned bool
	for _, s := range h.sender.out {
		warned = warned || strings.Contains(s.text, "just taken")
	}
	if !warned {
		t.Error("customer was not told the slot is taken")
	}
}

func TestForgedSlotRejected(t *testing.T) {
	h := newHarness(t)
	h.click(customer, "svc:2")
	h.click(customer, "day:2026-11-02")
	h.click(customer, slot(2, 3, 17)) // 03:17 — off-grid, outside working hours
	h.click(customer, "ok")
	if len(h.repo.bookings) != 0 {
		t.Fatal("booking outside working hours must be rejected")
	}
}

func TestCustomerCannotCancelSomeoneElsesBooking(t *testing.T) {
	h := newHarness(t)
	h.repo.CreateBooking(context.Background(), domain.Booking{
		UserID: 999, ChatID: 999, ServiceID: 1,
		StartsAt: time.Date(2026, 11, 3, 10, 0, 0, 0, loc),
		EndsAt:   time.Date(2026, 11, 3, 11, 0, 0, 0, loc),
	})
	h.click(customer, "cancel:1")
	if h.repo.bookings[0].Status != domain.StatusActive {
		t.Fatal("foreign booking was cancelled")
	}
}

func TestAdminOnlyActions(t *testing.T) {
	h := newHarness(t)
	h.repo.CreateBooking(context.Background(), domain.Booking{
		UserID: 999, ChatID: 999, ServiceID: 1,
		StartsAt: time.Date(2026, 11, 2, 10, 0, 0, 0, loc),
		EndsAt:   time.Date(2026, 11, 2, 11, 0, 0, 0, loc),
	})

	h.click(customer, "admx:1")
	if h.repo.bookings[0].Status != domain.StatusActive {
		t.Fatal("non-admin cancelled a booking")
	}

	h.click(admin, "admx:1")
	if h.repo.bookings[0].Status != domain.StatusCancelled {
		t.Fatal("admin could not cancel")
	}
	var notified bool
	for _, s := range h.sender.out {
		notified = notified || (s.chatID == 999 && strings.Contains(s.text, "cancelled by Test Shop"))
	}
	if !notified {
		t.Error("customer was not notified about admin cancellation")
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t)
	h.sess.blocked = true
	h.text(customer, btnBook)
	if got := h.sender.last().text; got != txtSlowDown {
		t.Fatalf("got %q", got)
	}
}

func TestChunks(t *testing.T) {
	got := chunks([]int{1, 2, 3, 4, 5}, 2)
	if fmt.Sprint(got) != "[[1 2] [3 4] [5]]" {
		t.Fatalf("got %v", got)
	}
}
