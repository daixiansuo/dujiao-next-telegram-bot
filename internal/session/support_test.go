package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/support"
)

func TestSupportRedisRestartAndEpochLoss(t *testing.T) {
	r, server := redisFixture(t)
	ctx := context.Background()
	store := r.SupportStore()
	_, err := store.Update(ctx, support.ModeKey(42), func(v *support.Record) error {
		*v = support.Record{Kind: "mode", UserID: 42, Status: "active", ExpiresAt: time.Now().Add(support.ModeLifetime)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(ctx, support.RouteKey(-100, 3), func(v *support.Record) error {
		*v = support.Record{Kind: "route", UserID: 42, SourceMessageID: 2, ExpiresAt: time.Now().Add(support.MessageLifetime)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := connectRedis(t, "redis://"+server.Addr()).SupportStore()
	if v, found, err := restarted.Get(ctx, support.RouteKey(-100, 3)); err != nil || !found || v.UserID != 42 {
		t.Fatal("route not persisted", err)
	}
	if v, found, err := restarted.Get(ctx, support.ModeKey(42)); err != nil || !found || v.Status != "active" {
		t.Fatal("mode not persisted", err)
	}
	server.FastForward(25 * time.Hour)
	if _, found, err := restarted.Get(ctx, support.ModeKey(42)); err != nil || found {
		t.Fatal("idle mode retained", err)
	}
	if _, found, err := restarted.Get(ctx, support.RouteKey(-100, 3)); err != nil || !found {
		t.Fatal("route expired too early", err)
	}
	server.FlushAll()
	if _, _, err := restarted.Get(ctx, support.RouteKey(-100, 3)); !errors.Is(err, support.ErrUnavailable) {
		t.Fatal("epoch loss not detected", err)
	}
}
func TestSupportRedisClosedModeDoesNotEraseReplyRoutes(t *testing.T) {
	r, server := redisFixture(t)
	ctx := context.Background()
	s := r.SupportStore()
	_, err := s.Update(ctx, support.ModeKey(42), func(v *support.Record) error {
		*v = support.Record{Kind: "mode", UserID: 42, Status: "active", ExpiresAt: time.Now().Add(support.ModeLifetime)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(ctx, support.ModeKey(42), func(v *support.Record) error { v.Status = "closed"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	server.FastForward(8 * 24 * time.Hour)
	if _, found, err := s.Get(ctx, support.ModeKey(42)); err != nil || found {
		t.Fatal("retention", err)
	}
}
