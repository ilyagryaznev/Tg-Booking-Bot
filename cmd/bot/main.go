package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed tz database so TIMEZONE works in minimal containers

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/yourname/tg-booking-bot/internal/bot"
	"github.com/yourname/tg-booking-bot/internal/config"
	"github.com/yourname/tg-booking-bot/internal/scheduling"
	"github.com/yourname/tg-booking-bot/internal/storage/postgres"
	"github.com/yourname/tg-booking-bot/internal/storage/redisstore"
	"github.com/yourname/tg-booking-bot/migrations"
)

const workers = 8

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		return err
	}

	rs, err := redisstore.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rs.Close()

	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return err
	}
	log.Info("authorized", "bot", api.Self.UserName)

	_, _ = api.Request(tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Main menu"},
		tgbotapi.BotCommand{Command: "book", Description: "Book a visit"},
		tgbotapi.BotCommand{Command: "my", Description: "My bookings"},
		tgbotapi.BotCommand{Command: "info", Description: "Prices and opening hours"},
	))

	b := bot.New(api, db, rs, bot.Settings{
		BusinessName: cfg.BusinessName,
		Hours: scheduling.Hours{
			Start: cfg.WorkStart, End: cfg.WorkEnd, Step: cfg.SlotStep, Location: cfg.Location,
		},
		BookingDays: cfg.BookingDays,
		Admins:      cfg.AdminIDs,
	}, log)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.RunReminders(ctx, cfg.ReminderLead, 30*time.Second)
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := api.GetUpdatesChan(u)

	// A fixed worker pool processes updates concurrently while capping DB load.
	jobs := make(chan tgbotapi.Update)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for upd := range jobs {
				hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				b.Handle(hctx, upd)
				cancel()
			}
		}()
	}

	log.Info("bot started", "workers", workers)
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case upd := <-updates:
			jobs <- upd
		}
	}

	log.Info("shutting down")
	api.StopReceivingUpdates()
	close(jobs)
	wg.Wait() // let in-flight updates finish
	return nil
}
