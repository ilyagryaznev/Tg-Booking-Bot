// Package config loads settings from environment variables (12-factor style).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken     string
	DatabaseURL  string
	RedisURL     string
	AdminIDs     map[int64]bool
	BusinessName string

	Location     *time.Location
	WorkStart    int           // opening hour, 0-23
	WorkEnd      int           // closing hour, 1-24
	SlotStep     time.Duration // grid step for start times
	BookingDays  int           // how many days ahead customers can book
	ReminderLead time.Duration // how long before the visit to remind
}

func Load() (*Config, error) {
	c := &Config{
		BotToken:     os.Getenv("BOT_TOKEN"),
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		RedisURL:     getenv("REDIS_URL", "redis://localhost:6379/0"),
		BusinessName: getenv("BUSINESS_NAME", "Demo Barbershop"),
		AdminIDs:     map[int64]bool{},
	}
	var errs []error

	if c.BotToken == "" {
		errs = append(errs, errors.New("BOT_TOKEN is required"))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}

	for _, raw := range strings.Split(os.Getenv("ADMIN_IDS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("ADMIN_IDS: invalid id %q", raw))
			continue
		}
		c.AdminIDs[id] = true
	}

	var err error
	if c.Location, err = time.LoadLocation(getenv("TIMEZONE", "UTC")); err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE: %w", err))
	}
	c.WorkStart = intEnv("WORK_START_HOUR", 10, &errs)
	c.WorkEnd = intEnv("WORK_END_HOUR", 20, &errs)
	c.BookingDays = intEnv("BOOKING_DAYS", 7, &errs)
	c.SlotStep = durEnv("SLOT_STEP", 30*time.Minute, &errs)
	c.ReminderLead = durEnv("REMINDER_LEAD", 2*time.Hour, &errs)

	if c.WorkStart < 0 || c.WorkEnd > 24 || c.WorkStart >= c.WorkEnd {
		errs = append(errs, fmt.Errorf("working hours %d-%d are invalid", c.WorkStart, c.WorkEnd))
	}
	if c.BookingDays < 1 || c.BookingDays > 30 {
		errs = append(errs, errors.New("BOOKING_DAYS must be between 1 and 30"))
	}
	if c.SlotStep < 5*time.Minute {
		errs = append(errs, errors.New("SLOT_STEP must be at least 5m"))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func (c *Config) IsAdmin(userID int64) bool { return c.AdminIDs[userID] }

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int, errs *[]error) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		return def
	}
	return n
}

func durEnv(key string, def time.Duration, errs *[]error) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		return def
	}
	return d
}
