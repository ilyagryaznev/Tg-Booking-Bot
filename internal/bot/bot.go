// Package bot contains Telegram update handlers: the customer booking flow,
// "my bookings", the admin schedule and reminder delivery.
package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/yourname/tg-booking-bot/internal/domain"
	"github.com/yourname/tg-booking-bot/internal/scheduling"
	"github.com/yourname/tg-booking-bot/internal/storage/redisstore"
)

// Repository is the persistence the bot needs (implemented by postgres.Store).
type Repository interface {
	ListServices(ctx context.Context) ([]domain.Service, error)
	GetService(ctx context.Context, id int) (domain.Service, error)
	BusyIntervals(ctx context.Context, from, to time.Time) ([]domain.Interval, error)
	CreateBooking(ctx context.Context, b domain.Booking) (int64, error)
	UpcomingForUser(ctx context.Context, userID int64, now time.Time) ([]domain.Booking, error)
	ActiveBetween(ctx context.Context, from, to time.Time) ([]domain.Booking, error)
	Cancel(ctx context.Context, id, userID int64) (domain.Booking, error)
	ClaimDueReminders(ctx context.Context, now time.Time, lead time.Duration) ([]domain.Booking, error)
}

// SessionStore keeps in-progress drafts and rate limits (implemented by redisstore.Store).
type SessionStore interface {
	SaveDraft(ctx context.Context, userID int64, d redisstore.Draft) error
	GetDraft(ctx context.Context, userID int64) (redisstore.Draft, bool, error)
	DeleteDraft(ctx context.Context, userID int64) error
	Allow(ctx context.Context, userID int64) (bool, error)
}

// Sender is the subset of *tgbotapi.BotAPI we use; it makes handlers testable.
type Sender interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
}

type Settings struct {
	BusinessName string
	Hours        scheduling.Hours
	BookingDays  int
	Admins       map[int64]bool
}

type Bot struct {
	api      Sender
	repo     Repository
	sessions SessionStore
	cfg      Settings
	log      *slog.Logger
	Now      func() time.Time // overridable in tests
}

func New(api Sender, repo Repository, sessions SessionStore, cfg Settings, log *slog.Logger) *Bot {
	return &Bot{api: api, repo: repo, sessions: sessions, cfg: cfg, log: log, Now: time.Now}
}

func (b *Bot) isAdmin(id int64) bool { return b.cfg.Admins[id] }

// Handle processes one Telegram update. Errors are logged and reported to the
// user in a friendly way; they never crash the update loop.
func (b *Bot) Handle(ctx context.Context, u tgbotapi.Update) {
	var (
		userID, chatID int64
		err            error
	)
	switch {
	case u.Message != nil && u.Message.From != nil:
		userID, chatID = u.Message.From.ID, u.Message.Chat.ID
		if !b.allow(ctx, userID, chatID) {
			return
		}
		err = b.onMessage(ctx, u.Message)
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		cq := u.CallbackQuery
		userID, chatID = cq.From.ID, cq.Message.Chat.ID
		// Always answer the callback so the button stops showing a spinner.
		if _, e := b.api.Request(tgbotapi.NewCallback(cq.ID, "")); e != nil {
			b.log.Warn("answer callback", "err", e)
		}
		if !b.allow(ctx, userID, chatID) {
			return
		}
		err = b.onCallback(ctx, cq)
	default:
		return
	}
	if err != nil {
		b.log.Error("handle update", "user", userID, "err", err)
		b.send(chatID, txtError, nil)
	}
}

func (b *Bot) allow(ctx context.Context, userID, chatID int64) bool {
	ok, err := b.sessions.Allow(ctx, userID)
	if err != nil {
		b.log.Warn("rate limiter", "err", err)
	}
	if !ok {
		b.send(chatID, txtSlowDown, nil)
	}
	return ok
}

// ---------- messages ----------

func (b *Bot) onMessage(ctx context.Context, m *tgbotapi.Message) error {
	cmd := m.Command()
	switch {
	case cmd == "start":
		name := html.EscapeString(m.From.FirstName)
		b.send(m.Chat.ID, fmt.Sprintf(txtWelcome, name, html.EscapeString(b.cfg.BusinessName)), b.mainMenu(m.From.ID))
		return nil
	case cmd == "book" || m.Text == btnBook:
		return b.showServices(ctx, m.Chat.ID, 0)
	case cmd == "my" || m.Text == btnMy:
		return b.showMyBookings(ctx, m.Chat.ID, m.From.ID)
	case cmd == "info" || m.Text == btnInfo:
		return b.showInfo(ctx, m.Chat.ID)
	case (cmd == "admin" || m.Text == btnSchedule) && b.isAdmin(m.From.ID):
		b.send(m.Chat.ID, txtAdminPick, adminPeriods())
		return nil
	default:
		b.send(m.Chat.ID, txtHelp, b.mainMenu(m.From.ID))
		return nil
	}
}

func (b *Bot) mainMenu(userID int64) tgbotapi.ReplyKeyboardMarkup {
	rows := [][]tgbotapi.KeyboardButton{
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnBook)),
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnMy), tgbotapi.NewKeyboardButton(btnInfo)),
	}
	if b.isAdmin(userID) {
		rows = append(rows, tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnSchedule)))
	}
	kb := tgbotapi.NewReplyKeyboard(rows...)
	kb.ResizeKeyboard = true
	return kb
}

// ---------- callbacks ----------

func (b *Bot) onCallback(ctx context.Context, cq *tgbotapi.CallbackQuery) error {
	chatID, msgID, userID := cq.Message.Chat.ID, cq.Message.MessageID, cq.From.ID
	action, arg, _ := strings.Cut(cq.Data, ":")

	switch action {
	case "svc":
		id, err := strconv.Atoi(arg)
		if err != nil {
			return nil // ignore malformed callback data
		}
		return b.pickService(ctx, chatID, msgID, userID, id)
	case "day":
		return b.pickDay(ctx, chatID, msgID, userID, arg)
	case "time":
		ts, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return nil
		}
		return b.pickTime(ctx, chatID, msgID, userID, ts)
	case "ok":
		return b.confirm(ctx, cq)
	case "back":
		d, ok, err := b.sessions.GetDraft(ctx, userID)
		if err != nil {
			return err
		}
		if arg == "days" && ok {
			return b.pickService(ctx, chatID, msgID, userID, d.ServiceID)
		}
		return b.showServices(ctx, chatID, msgID)
	case "x":
		if err := b.sessions.DeleteDraft(ctx, userID); err != nil {
			return err
		}
		b.edit(chatID, msgID, txtAborted, nil)
		return nil
	case "cancel":
		id, _ := strconv.ParseInt(arg, 10, 64)
		return b.customerCancel(ctx, chatID, msgID, userID, id)
	case "adm":
		if !b.isAdmin(userID) {
			return nil
		}
		days, _ := strconv.Atoi(arg)
		return b.adminSchedule(ctx, chatID, msgID, days)
	case "admx":
		if !b.isAdmin(userID) {
			return nil
		}
		id, _ := strconv.ParseInt(arg, 10, 64)
		return b.adminCancel(ctx, chatID, msgID, id)
	}
	return nil
}

// ---------- booking flow ----------

func (b *Bot) showServices(ctx context.Context, chatID int64, editMsgID int) error {
	services, err := b.repo.ListServices(ctx)
	if err != nil {
		return err
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, s := range services {
		label := fmt.Sprintf("%s · %d min · %s", s.Name, s.DurationMin, s.PriceString())
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("svc:%d", s.ID))))
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	if editMsgID != 0 {
		b.edit(chatID, editMsgID, txtChooseService, &kb)
	} else {
		b.send(chatID, txtChooseService, kb)
	}
	return nil
}

// freeSlotsByDay computes free start times for every bookable day with one DB query.
func (b *Bot) freeSlotsByDay(ctx context.Context, svc domain.Service) ([]time.Time, map[string][]time.Time, error) {
	now := b.Now()
	days := scheduling.Days(now, b.cfg.BookingDays, b.cfg.Hours.Location)
	from := days[0]
	to := days[len(days)-1].AddDate(0, 0, 1)
	busy, err := b.repo.BusyIntervals(ctx, from, to)
	if err != nil {
		return nil, nil, err
	}
	slots := make(map[string][]time.Time, len(days))
	var open []time.Time
	for _, d := range days {
		free := scheduling.FreeSlots(b.cfg.Hours, d, svc.Duration(), busy, now)
		if len(free) > 0 {
			slots[d.Format(time.DateOnly)] = free
			open = append(open, d)
		}
	}
	return open, slots, nil
}

func (b *Bot) pickService(ctx context.Context, chatID int64, msgID int, userID int64, serviceID int) error {
	svc, err := b.repo.GetService(ctx, serviceID)
	if errors.Is(err, domain.ErrNotFound) {
		return b.showServices(ctx, chatID, msgID)
	}
	if err != nil {
		return err
	}
	if err := b.sessions.SaveDraft(ctx, userID, redisstore.Draft{ServiceID: svc.ID}); err != nil {
		return err
	}
	days, _, err := b.freeSlotsByDay(ctx, svc)
	if err != nil {
		return err
	}
	back := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(btnBack, "back:svc"))
	if len(days) == 0 {
		kb := tgbotapi.NewInlineKeyboardMarkup(back)
		b.edit(chatID, msgID, fmt.Sprintf(txtNoDays, html.EscapeString(svc.Name), b.cfg.BookingDays), &kb)
		return nil
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, chunk := range chunks(days, 2) {
		var row []tgbotapi.InlineKeyboardButton
		for _, d := range chunk {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(b.dayLabel(d), "day:"+d.Format(time.DateOnly)))
		}
		rows = append(rows, row)
	}
	rows = append(rows, back)
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	b.edit(chatID, msgID, fmt.Sprintf(txtChooseDay, html.EscapeString(svc.Name)), &kb)
	return nil
}

func (b *Bot) pickDay(ctx context.Context, chatID int64, msgID int, userID int64, day string) error {
	d, ok, err := b.sessions.GetDraft(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		b.edit(chatID, msgID, txtExpired, nil)
		return b.showServices(ctx, chatID, 0)
	}
	svc, err := b.repo.GetService(ctx, d.ServiceID)
	if err != nil {
		return err
	}
	_, slots, err := b.freeSlotsByDay(ctx, svc)
	if err != nil {
		return err
	}
	back := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(btnBack, "back:days"))
	free := slots[day]
	if len(free) == 0 {
		kb := tgbotapi.NewInlineKeyboardMarkup(back)
		b.edit(chatID, msgID, txtNoTimes, &kb)
		return nil
	}
	d.Day = day
	if err := b.sessions.SaveDraft(ctx, userID, d); err != nil {
		return err
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, chunk := range chunks(free, 4) {
		var row []tgbotapi.InlineKeyboardButton
		for _, t := range chunk {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(
				t.In(b.cfg.Hours.Location).Format("15:04"), fmt.Sprintf("time:%d", t.Unix())))
		}
		rows = append(rows, row)
	}
	rows = append(rows, back)
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	b.edit(chatID, msgID, fmt.Sprintf(txtChooseTime, html.EscapeString(svc.Name), b.dayLabel(free[0])), &kb)
	return nil
}

func (b *Bot) pickTime(ctx context.Context, chatID int64, msgID int, userID int64, ts int64) error {
	d, ok, err := b.sessions.GetDraft(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		b.edit(chatID, msgID, txtExpired, nil)
		return b.showServices(ctx, chatID, 0)
	}
	svc, err := b.repo.GetService(ctx, d.ServiceID)
	if err != nil {
		return err
	}
	d.StartUnix = ts
	if err := b.sessions.SaveDraft(ctx, userID, d); err != nil {
		return err
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(btnConfirm, "ok"),
			tgbotapi.NewInlineKeyboardButtonData(btnAbort, "x")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(btnBack, "day:"+d.Day)),
	)
	text := fmt.Sprintf(txtConfirm, html.EscapeString(svc.Name), svc.DurationMin, svc.PriceString(),
		b.timeLabel(time.Unix(ts, 0)))
	b.edit(chatID, msgID, text, &kb)
	return nil
}

func (b *Bot) confirm(ctx context.Context, cq *tgbotapi.CallbackQuery) error {
	chatID, msgID, userID := cq.Message.Chat.ID, cq.Message.MessageID, cq.From.ID
	d, ok, err := b.sessions.GetDraft(ctx, userID)
	if err != nil {
		return err
	}
	if !ok || d.StartUnix == 0 {
		b.edit(chatID, msgID, txtExpired, nil)
		return b.showServices(ctx, chatID, 0)
	}
	svc, err := b.repo.GetService(ctx, d.ServiceID)
	if err != nil {
		return err
	}

	// Never trust callback data: re-check that the time is a real free slot
	// (within working hours, on the grid, in the future).
	start := time.Unix(d.StartUnix, 0).In(b.cfg.Hours.Location)
	_, slots, err := b.freeSlotsByDay(ctx, svc)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(slots[start.Format(time.DateOnly)], start.Equal) {
		return b.slotTaken(ctx, chatID, msgID, userID, d)
	}

	booking := domain.Booking{
		UserID:       userID,
		ChatID:       chatID,
		CustomerName: customerName(cq.From),
		ServiceID:    svc.ID,
		ServiceName:  svc.Name,
		StartsAt:     start,
		EndsAt:       start.Add(svc.Duration()),
	}
	id, err := b.repo.CreateBooking(ctx, booking)
	if errors.Is(err, domain.ErrSlotTaken) {
		return b.slotTaken(ctx, chatID, msgID, userID, d)
	}
	if err != nil {
		return err
	}
	if err := b.sessions.DeleteDraft(ctx, userID); err != nil {
		b.log.Warn("delete draft", "err", err)
	}
	b.edit(chatID, msgID, fmt.Sprintf(txtBooked, html.EscapeString(svc.Name), b.timeLabel(start)), nil)
	b.log.Info("booking created", "id", id, "user", userID, "service", svc.ID, "start", start)
	b.notifyAdmins(fmt.Sprintf(txtAdminNew, id, html.EscapeString(booking.CustomerName),
		html.EscapeString(svc.Name), b.timeLabel(start)))
	return nil
}

func (b *Bot) slotTaken(ctx context.Context, chatID int64, msgID int, userID int64, d redisstore.Draft) error {
	b.send(chatID, txtSlotTaken, nil)
	return b.pickDay(ctx, chatID, msgID, userID, d.Day)
}

// ---------- my bookings ----------

func (b *Bot) showMyBookings(ctx context.Context, chatID, userID int64) error {
	list, err := b.repo.UpcomingForUser(ctx, userID, b.Now())
	if err != nil {
		return err
	}
	if len(list) == 0 {
		b.send(chatID, txtNoBookings, nil)
		return nil
	}
	var sb strings.Builder
	sb.WriteString(txtYourBookings + "\n\n")
	var rows [][]tgbotapi.InlineKeyboardButton
	for i, bk := range list {
		fmt.Fprintf(&sb, "%d. %s — <b>%s</b>\n", i+1, html.EscapeString(bk.ServiceName), b.timeLabel(bk.StartsAt))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(
			fmt.Sprintf("❌ Cancel #%d (%s)", i+1, b.shortLabel(bk.StartsAt)), fmt.Sprintf("cancel:%d", bk.ID))))
	}
	b.send(chatID, sb.String(), tgbotapi.NewInlineKeyboardMarkup(rows...))
	return nil
}

func (b *Bot) customerCancel(ctx context.Context, chatID int64, msgID int, userID, id int64) error {
	bk, err := b.repo.Cancel(ctx, id, userID)
	if errors.Is(err, domain.ErrNotFound) {
		b.edit(chatID, msgID, txtNotFound, nil)
		return nil
	}
	if err != nil {
		return err
	}
	b.edit(chatID, msgID, fmt.Sprintf(txtCancelled, html.EscapeString(bk.ServiceName), b.timeLabel(bk.StartsAt)), nil)
	b.notifyAdmins(fmt.Sprintf(txtAdminCancelled, bk.ID, html.EscapeString(bk.CustomerName),
		html.EscapeString(bk.ServiceName), b.timeLabel(bk.StartsAt)))
	return nil
}

// ---------- info ----------

func (b *Bot) showInfo(ctx context.Context, chatID int64) error {
	services, err := b.repo.ListServices(ctx)
	if err != nil {
		return err
	}
	var sb strings.Builder
	for _, s := range services {
		fmt.Fprintf(&sb, "• %s — %d min, %s\n", html.EscapeString(s.Name), s.DurationMin, s.PriceString())
	}
	b.send(chatID, fmt.Sprintf(txtInfo, html.EscapeString(b.cfg.BusinessName),
		b.cfg.Hours.Start, b.cfg.Hours.End, sb.String()), nil)
	return nil
}

// ---------- admin ----------

func adminPeriods() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("Today", "adm:0"),
		tgbotapi.NewInlineKeyboardButtonData("Tomorrow", "adm:1"),
		tgbotapi.NewInlineKeyboardButtonData("7 days", "adm:7"),
	))
}

func (b *Bot) adminSchedule(ctx context.Context, chatID int64, msgID, period int) error {
	loc := b.cfg.Hours.Location
	today := scheduling.Days(b.Now(), 1, loc)[0]
	from, to, title := today, today.AddDate(0, 0, 1), "today"
	switch period {
	case 1:
		from, to, title = today.AddDate(0, 0, 1), today.AddDate(0, 0, 2), "tomorrow"
	case 7:
		to, title = today.AddDate(0, 0, 7), "the next 7 days"
	}
	list, err := b.repo.ActiveBetween(ctx, from, to)
	if err != nil {
		return err
	}
	kb := adminPeriods()
	if len(list) == 0 {
		b.edit(chatID, msgID, fmt.Sprintf(txtAdminEmpty, title), &kb)
		return nil
	}
	var sb strings.Builder
	for _, bk := range list {
		fmt.Fprintf(&sb, "#%d · <b>%s–%s</b> · %s\n     👤 %s\n",
			bk.ID, b.timeLabel(bk.StartsAt), bk.EndsAt.In(loc).Format("15:04"),
			html.EscapeString(bk.ServiceName), html.EscapeString(bk.CustomerName))
		kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("❌ Cancel #%d", bk.ID), fmt.Sprintf("admx:%d", bk.ID))))
	}
	b.edit(chatID, msgID, fmt.Sprintf(txtAdminList, title, sb.String()), &kb)
	return nil
}

func (b *Bot) adminCancel(ctx context.Context, chatID int64, msgID int, id int64) error {
	bk, err := b.repo.Cancel(ctx, id, 0)
	if errors.Is(err, domain.ErrNotFound) {
		b.edit(chatID, msgID, txtNotFound, nil)
		return nil
	}
	if err != nil {
		return err
	}
	b.send(bk.ChatID, fmt.Sprintf(txtCancelledByBiz, html.EscapeString(b.cfg.BusinessName),
		html.EscapeString(bk.ServiceName), b.timeLabel(bk.StartsAt)), nil)
	return b.adminSchedule(ctx, chatID, msgID, 7)
}

func (b *Bot) notifyAdmins(text string) {
	for id := range b.cfg.Admins {
		b.send(id, text, nil)
	}
}

// ---------- reminders ----------

// RunReminders periodically sends reminders until ctx is cancelled.
func (b *Bot) RunReminders(ctx context.Context, lead, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		b.sendDueReminders(ctx, lead)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) sendDueReminders(ctx context.Context, lead time.Duration) {
	list, err := b.repo.ClaimDueReminders(ctx, b.Now(), lead)
	if err != nil {
		if ctx.Err() == nil {
			b.log.Error("claim reminders", "err", err)
		}
		return
	}
	for _, bk := range list {
		b.send(bk.ChatID, fmt.Sprintf(txtReminder, html.EscapeString(b.cfg.BusinessName),
			html.EscapeString(bk.ServiceName), b.timeLabel(bk.StartsAt)), nil)
		b.log.Info("reminder sent", "booking", bk.ID)
	}
}

// ---------- helpers ----------

func (b *Bot) send(chatID int64, text string, markup any) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	if _, err := b.api.Send(msg); err != nil {
		b.log.Warn("send message", "chat", chatID, "err", err)
	}
}

func (b *Bot) edit(chatID int64, msgID int, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	e := tgbotapi.NewEditMessageText(chatID, msgID, text)
	e.ParseMode = tgbotapi.ModeHTML
	e.ReplyMarkup = kb
	if _, err := b.api.Send(e); err != nil {
		b.log.Warn("edit message", "chat", chatID, "err", err)
	}
}

func (b *Bot) dayLabel(t time.Time) string {
	return t.In(b.cfg.Hours.Location).Format("Mon, 02 Jan")
}

func (b *Bot) shortLabel(t time.Time) string {
	return t.In(b.cfg.Hours.Location).Format("02 Jan 15:04")
}

func (b *Bot) timeLabel(t time.Time) string {
	return t.In(b.cfg.Hours.Location).Format("Mon, 02 Jan · 15:04")
}

func customerName(u *tgbotapi.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.UserName != "" {
		name += " (@" + u.UserName + ")"
	}
	if name == "" {
		name = fmt.Sprintf("user %d", u.ID)
	}
	return name
}

func chunks[T any](s []T, n int) [][]T {
	var out [][]T
	for n < len(s) {
		s, out = s[n:], append(out, s[:n:n])
	}
	return append(out, s)
}
