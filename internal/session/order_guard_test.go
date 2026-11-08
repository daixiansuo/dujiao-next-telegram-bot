package session

import (
	"testing"
	"time"
)

func TestOrderGuardSurvivesRedisRestartDraftExpiryAndNavigation(t *testing.T) {
	r, server := redisFixture(t)
	s := r.Update(42, func(s *State) {
		s.Flow = "manual_form"
		s.Purchase = &PurchaseDraft{ID: "private-input", ExpiresAt: time.Now().Add(time.Second)}
		s.OrderGuard = &OrderCreateGuard{ID: "create-attempt", Status: "posting", LeaseUntil: time.Now().Add(-time.Second)}
	})
	if s.Err != nil {
		t.Fatal(s.Err)
	}
	server.FastForward(2 * time.Second)
	fresh := connectRedis(t, "redis://"+server.Addr())
	if err := fresh.ResetFlow(42); err != nil {
		t.Fatal(err)
	}
	got := fresh.Get(42)
	if got.Err != nil || got.Purchase != nil || got.OrderGuard == nil || got.OrderGuard.ID != "create-attempt" || got.OrderGuard.Status != "unknown" {
		t.Fatalf("guard was lost: %+v", got)
	}
	got.OrderGuard.ID = "mutated-copy"
	if fresh.Get(42).OrderGuard.ID != "create-attempt" {
		t.Fatal("session returned shared guard")
	}
	server.FlushAll()
	if fresh.Get(42).Err == nil {
		t.Fatal("cleared dataset was accepted")
	}
}

func TestDefaultLocaleRefreshPreservesSavedChoices(t *testing.T) {
	r, _ := redisFixture(t)
	for _, store := range []interface {
		Store
		SetDefaultLocale(string)
	}{NewMemory("zh-CN"), r} {
		if s := store.Update(42, func(s *State) { s.Locale = "zh-TW" }); s.Err != nil {
			t.Fatal(s.Err)
		}
		store.SetDefaultLocale("en-US")
		if store.Get(42).Locale != "zh-TW" || store.Get(43).Locale != "en-US" {
			t.Fatal("refresh changed a saved choice or missed a new user")
		}
	}
}
