//go:build integration

package redisstore

import (
	"context"
	"os"
	"testing"
)

func TestDraftsAndRateLimit(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.rdb.FlushDB(ctx)

	if _, ok, _ := s.GetDraft(ctx, 1); ok {
		t.Fatal("unexpected draft")
	}
	want := Draft{ServiceID: 3, Day: "2026-11-05", StartUnix: 1793000000}
	if err := s.SaveDraft(ctx, 1, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetDraft(ctx, 1)
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if ttl := s.rdb.TTL(ctx, draftKey(1)).Val(); ttl <= 0 {
		t.Fatalf("draft must expire, ttl=%v", ttl)
	}
	s.DeleteDraft(ctx, 1)
	if _, ok, _ := s.GetDraft(ctx, 1); ok {
		t.Fatal("draft not deleted")
	}

	allowed := 0
	for i := 0; i < 20; i++ {
		if ok, _ := s.Allow(ctx, 42); ok {
			allowed++
		}
	}
	// A second boundary may fall inside the loop, so allow up to 2 windows.
	if allowed < s.rate || allowed > 2*s.rate {
		t.Fatalf("allowed %d of 20, limit %d/s", allowed, s.rate)
	}
}
