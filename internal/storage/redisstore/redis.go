// Package redisstore keeps short-lived conversation state and rate limits in Redis.
package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Draft is a booking the customer is still assembling step by step.
type Draft struct {
	ServiceID int    `json:"service_id"`
	Day       string `json:"day,omitempty"`   // YYYY-MM-DD
	StartUnix int64  `json:"start,omitempty"` // chosen start time
}

type Store struct {
	rdb      *redis.Client
	draftTTL time.Duration
	rate     int // max updates per user per second
}

func Open(ctx context.Context, url string) (*Store, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis is unreachable: %w", err)
	}
	return &Store{rdb: rdb, draftTTL: 15 * time.Minute, rate: 5}, nil
}

func (s *Store) Close() error { return s.rdb.Close() }

func draftKey(userID int64) string { return fmt.Sprintf("draft:%d", userID) }

func (s *Store) SaveDraft(ctx context.Context, userID int64, d Draft) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, draftKey(userID), b, s.draftTTL).Err()
}

// GetDraft returns (draft, true) or (zero, false) if it expired or never existed.
func (s *Store) GetDraft(ctx context.Context, userID int64) (Draft, bool, error) {
	var d Draft
	b, err := s.rdb.Get(ctx, draftKey(userID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	return d, true, json.Unmarshal(b, &d)
}

func (s *Store) DeleteDraft(ctx context.Context, userID int64) error {
	return s.rdb.Del(ctx, draftKey(userID)).Err()
}

// Allow implements a fixed-window rate limiter (N events per second per user)
// that protects the bot and the database from button spam.
func (s *Store) Allow(ctx context.Context, userID int64) (bool, error) {
	key := fmt.Sprintf("rl:%d:%d", userID, time.Now().Unix())
	pipe := s.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 2*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return true, err // fail open: never block customers because of Redis hiccups
	}
	return incr.Val() <= int64(s.rate), nil
}
