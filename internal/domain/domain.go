// Package domain holds the core business types shared by all layers.
package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrSlotTaken = errors.New("slot is already taken")
)

type Service struct {
	ID          int
	Name        string
	DurationMin int
	PriceCents  int
}

func (s Service) Duration() time.Duration { return time.Duration(s.DurationMin) * time.Minute }

// PriceString formats the price as "$30" or "$12.50".
func (s Service) PriceString() string {
	if s.PriceCents%100 == 0 {
		return fmt.Sprintf("$%d", s.PriceCents/100)
	}
	return fmt.Sprintf("$%d.%02d", s.PriceCents/100, s.PriceCents%100)
}

type BookingStatus string

const (
	StatusActive    BookingStatus = "active"
	StatusCancelled BookingStatus = "cancelled"
)

type Booking struct {
	ID           int64
	UserID       int64
	ChatID       int64
	CustomerName string
	ServiceID    int
	ServiceName  string
	StartsAt     time.Time
	EndsAt       time.Time
	Status       BookingStatus
}

// Interval is a half-open time range [Start, End).
type Interval struct {
	Start, End time.Time
}

func (i Interval) Overlaps(o Interval) bool {
	return i.Start.Before(o.End) && o.Start.Before(i.End)
}
