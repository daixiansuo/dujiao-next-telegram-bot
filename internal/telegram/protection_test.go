package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func TestPausedBotBlocksCommandsAndOldCallbacksButKeepsTransactionNotices(t *testing.T) {
	a, f := newFormFixture(t, nil)
	a.UpdateConfig(dujiao.BotConfig{Enabled: false})
	a.handleUpdate(context.Background(), nil, giftCardMessage("/wallet", 0, 42, 42))
	formCallback(a, 42, 10, "wallet")
	if err := a.Notify(context.Background(), 42, "order-paid", callback.Event{OrderID: 7}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	text := strings.Join(f.texts, "\n")
	if strings.Contains(text, "150.00") || !strings.Contains(text, "订单支付成功") {
		t.Fatal("paused access or paid notification semantics changed")
	}
}

func TestPrivateAccountActionsNeverSendToGroups(t *testing.T) {
	a, f := newFormFixture(t, nil)
	for _, command := range []string{"/wallet", "/orders", "/recharge", "/start"} {
		u := giftCardMessage(command, 0, 42, -1001)
		u.Message.Chat.Type = models.ChatTypeSupergroup
		a.handleUpdate(context.Background(), nil, u)
	}
	a.handleCallback(context.Background(), &models.CallbackQuery{ID: "group", From: models.User{ID: 42}, Data: "wallet", Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup}}}})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) != 0 {
		t.Fatal("account content was sent to group")
	}
}

func TestLostCreateResponseRequiresOrderReviewBeforeAnotherSubmission(t *testing.T) {
	a, f := newFormFixture(t, nil)
	var creates atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch r.URL.Path {
		case "/api/v1/channel/catalog/products/3":
			result = f.product
		case "/api/v1/channel/orders/preview":
			result = dujiao.OrderPreview{Valid: true, TotalAmount: "10.00", Currency: "CNY"}
		case "/api/v1/channel/orders":
			if r.Method == "GET" {
				result = dujiao.OrderList{TotalPages: 1}
				break
			}
			creates.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": result})
	}))
	defer srv.Close()
	a.api, _ = dujiao.NewClient(srv.URL, "test-key", "test-secret", time.Second)
	formCallback(a, 42, 10, "buy:3:31:1")
	d := a.sessions.Get(42).Purchase
	if d == nil || !d.Ready {
		t.Fatal("missing preview")
	}
	formCallback(a, 42, d.PreviewID, fmt.Sprintf("formconfirm:%s:%d", d.ID, d.Revision))
	state := a.sessions.Get(42)
	if creates.Load() != 1 || state.OrderGuard == nil || state.OrderGuard.Status != "unknown" {
		t.Fatal("lost response was not protected")
	}
	formCallback(a, 42, 11, "buy:3:31:1")
	formCallback(a, 42, 11, "orderack:"+state.OrderGuard.ID)
	if creates.Load() != 1 || a.sessions.Get(42).OrderGuard == nil {
		t.Fatal("navigation or unreviewed button bypassed guard")
	}
	if err := a.sessions.ResetFlow(42); err != nil {
		t.Fatal(err)
	}
	a.markOrderGuardReviewed(context.Background(), 42, "older-guard")
	if a.sessions.Get(42).OrderGuard.ReviewMessageID != 0 {
		t.Fatal("old query authorized a newer guard")
	}
	formCallback(a, 42, 12, "orders:a:1")
	review := a.sessions.Get(42).OrderGuard
	if review.ReviewMessageID == 0 {
		t.Fatal("no review confirmation")
	}
	formCallback(a, 42, review.ReviewMessageID+1, "orderack:"+review.ID)
	if a.sessions.Get(42).OrderGuard == nil {
		t.Fatal("wrong review message unlocked")
	}
	formCallback(a, 42, review.ReviewMessageID, "orderack:"+review.ID)
	if a.sessions.Get(42).OrderGuard != nil || creates.Load() != 1 {
		t.Fatal("manual unlock resent an order")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(strings.Join(f.texts, "\n"), "订单创建失败") {
		t.Fatal("unknown result was described as definite failure")
	}
}

func TestOrderCreationRejectsReplacedDraftAndClassifiesErrors(t *testing.T) {
	a, _ := newFormFixture(t, nil)
	a.sessions.Update(42, func(s *session.State) {
		s.Purchase = &session.PurchaseDraft{ID: "new", Submitting: true, ExpiresAt: time.Now().Add(time.Hour)}
	})
	if _, err := a.beginOrderCreation(42, "old"); err == nil {
		t.Fatal("replaced draft claimed a write")
	}
	for _, tc := range []struct {
		status  int
		unknown bool
	}{{400, false}, {403, false}, {408, true}, {429, true}, {500, true}} {
		if orderCreationUnknown(&dujiao.APIError{HTTPStatus: tc.status}) != tc.unknown {
			t.Fatal("incorrect certainty", tc.status)
		}
	}
}

func TestSupportMediaUsesMainRouterAndPausedGroupCannotUseUnsubscribeException(t *testing.T) {
	a, f := newSupportFixture(t)
	ctx := context.Background()
	a.openSupport(ctx, 42, 42, 0)
	media := supportMessageFixture(42, 42, 10, "")
	media.Document = &models.Document{FileID: "synthetic"}
	a.handleUpdate(ctx, nil, &models.Update{Message: media})
	f.mu.Lock()
	copies := append([]supportCopy(nil), f.copies...)
	f.mu.Unlock()
	if len(copies) != 1 || copies[0].to != -100 {
		t.Fatal("media was lost before support routing")
	}
	a.UpdateConfig(dujiao.BotConfig{Enabled: false})
	reply := supportMessageFixture(900, -100, 20, "/unsubscribe")
	reply.ReplyToMessage = &models.Message{ID: copies[0].result}
	a.handleUpdate(ctx, nil, &models.Update{Message: reply})
	f.mu.Lock()
	count := len(f.copies)
	f.mu.Unlock()
	if count != 1 {
		t.Fatal("group reply bypassed pause")
	}
	a.UpdateConfig(dujiao.BotConfig{Enabled: true})
	a.handleUpdate(ctx, nil, &models.Update{Message: reply})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.copies) != 2 || f.copies[1].to != 42 {
		t.Fatal("authorized reply not routed after resume")
	}
}
