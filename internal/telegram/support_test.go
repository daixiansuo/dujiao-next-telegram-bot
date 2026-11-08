package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/dunea/dujiao-next-telegram-bot/internal/support"
	"github.com/go-telegram/bot/models"
)

type supportCopy struct {
	to, from       int64
	source, result int
	markup         string
}
type supportFixture struct {
	mu       sync.Mutex
	copies   []supportCopy
	methods  []string
	texts    []string
	id       int
	copyCode int
	drop     bool
	orders   []string
}

func newSupportFixture(t *testing.T) (*App, *supportFixture) {
	t.Helper()
	f := &supportFixture{id: 1000}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1/channel/orders/") {
			f.mu.Lock()
			f.orders = append(f.orders, r.URL.RequestURI())
			f.mu.Unlock()
			if r.URL.Query().Get("channel_user_id") != "42" || !strings.HasSuffix(r.URL.Path, "/7") {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"status_code":404,"error_code":"order_not_found"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": dujiao.OrderDetail{OrderID: 7, OrderNo: "SYNTHETIC-7", Status: "completed"}})
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if method == "getMe" {
			fmt.Fprint(w, `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Test"}}`)
			return
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.id++
		f.methods = append(f.methods, method)
		switch method {
		case "copyMessage":
			to, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
			from, _ := strconv.ParseInt(r.FormValue("from_chat_id"), 10, 64)
			mid, _ := strconv.Atoi(r.FormValue("message_id"))
			f.copies = append(f.copies, supportCopy{to, from, mid, f.id, r.FormValue("reply_markup")})
			if f.drop {
				c, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
				} else {
					_ = c.Close()
				}
				return
			}
			if f.copyCode != 0 {
				w.WriteHeader(f.copyCode)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": f.copyCode, "description": "synthetic send failed"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]int{"message_id": f.id}})
		case "sendMessage", "editMessageText":
			f.texts = append(f.texts, r.FormValue("text"))
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": f.id, "chat": map[string]any{"id": 42, "type": "private"}}})
		case "answerCallbackQuery":
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			t.Errorf("unexpected Telegram method %s", method)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	api, err := dujiao.NewClient(server.URL, "synthetic-key", "synthetic-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New("999:synthetic", api, dujiao.BotConfig{Enabled: true}, "en-US", time.Second, server.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ConfigureSupport(-100, []int64{900}); err != nil {
		t.Fatal(err)
	}
	return a, f
}
func supportMessageFixture(userID, chatID int64, id int, text string) *models.Message {
	return &models.Message{ID: id, From: &models.User{ID: userID}, Chat: models.Chat{ID: chatID}, Text: text}
}
func TestHumanSupportMultipleCustomersMediaAndRepliesAfterExit(t *testing.T) {
	a, f := newSupportFixture(t)
	ctx := context.Background()
	a.openSupport(ctx, 42, 42, 0)
	a.openSupport(ctx, 43, 43, 0)
	one := supportMessageFixture(42, 42, 11, "")
	one.Photo = []models.PhotoSize{{FileID: "synthetic-photo"}}
	two := supportMessageFixture(43, 43, 12, "")
	two.Document = &models.Document{FileID: "synthetic-file"}
	if !a.handleSupportMessage(ctx, one) || !a.handleSupportMessage(ctx, two) {
		t.Fatal("media not handled")
	}
	f.mu.Lock()
	copies := append([]supportCopy(nil), f.copies...)
	f.mu.Unlock()
	if len(copies) != 2 || copies[0].to != -100 || copies[0].from != 42 || copies[1].from != 43 {
		t.Fatal("customer routing", copies)
	}
	if !strings.Contains(copies[0].markup, "42") || !strings.Contains(copies[1].markup, "43") {
		t.Fatal("customer markers missing")
	}
	if err := a.stopSupport(ctx, 42); err != nil {
		t.Fatal(err)
	}
	reply := supportMessageFixture(900, -100, 21, "answer")
	reply.ReplyToMessage = &models.Message{ID: copies[0].result}
	a.handleSupportMessage(ctx, reply)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.copies) != 3 || f.copies[2].to != 42 || f.copies[2].from != -100 {
		t.Fatal("reply after mode exit routed incorrectly", f.copies)
	}
}
func TestHumanSupportRejectsUntrustedStaffAndMissingRoutes(t *testing.T) {
	a, f := newSupportFixture(t)
	ctx := context.Background()
	a.openSupport(ctx, 42, 42, 0)
	a.handleSupportMessage(ctx, supportMessageFixture(42, 42, 10, "question"))
	f.mu.Lock()
	id := f.copies[0].result
	f.mu.Unlock()
	for _, m := range []*models.Message{
		supportMessageFixture(901, -100, 20, "unauthorized"), supportMessageFixture(900, -101, 21, "wrong group"), supportMessageFixture(900, -100, 22, "missing route"),
	} {
		m.ReplyToMessage = &models.Message{ID: id + 1}
		a.handleSupportMessage(ctx, m)
	}
	anonymous := supportMessageFixture(900, -100, 23, "anonymous")
	anonymous.SenderChat = &models.Chat{ID: -100}
	anonymous.ReplyToMessage = &models.Message{ID: id}
	a.handleSupportMessage(ctx, anonymous)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.copies) != 1 {
		t.Fatal("untrusted staff forwarded", f.copies)
	}
}
func TestHumanSupportDuplicateAndUnknownSendsAreNotRepeated(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprint(drop), func(t *testing.T) {
			a, f := newSupportFixture(t)
			ctx := context.Background()
			a.openSupport(ctx, 42, 42, 0)
			f.mu.Lock()
			f.drop = drop
			f.mu.Unlock()
			m := supportMessageFixture(42, 42, 10, "question")
			a.handleSupportMessage(ctx, m)
			a.handleSupportMessage(ctx, m)
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.copies) != 1 {
				t.Fatal("duplicate send", f.copies)
			}
			if drop && !strings.Contains(strings.Join(f.texts, "\n"), "could not be confirmed") {
				t.Fatal("unknown result not explained")
			}
		})
	}
}
func TestHumanSupportExplicitSendFailureAllowsDeliberateRetry(t *testing.T) {
	a, f := newSupportFixture(t)
	ctx := context.Background()
	a.openSupport(ctx, 42, 42, 0)
	m := supportMessageFixture(42, 42, 10, "question")
	f.mu.Lock()
	f.copyCode = 403
	f.mu.Unlock()
	a.handleSupportMessage(ctx, m)
	f.mu.Lock()
	f.copyCode = 0
	f.mu.Unlock()
	a.handleSupportMessage(ctx, m)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.copies) != 2 {
		t.Fatal("explicit failure not retryable")
	}
}
func TestHumanSupportEnteringClearsInputButKeepsFinancialProtection(t *testing.T) {
	a, _ := newSupportFixture(t)
	ctx := context.Background()
	a.sessions.Update(42, func(s *session.State) {
		s.Flow = "manual_form"
		s.Purchase = &session.PurchaseDraft{ID: "draft", ExpiresAt: time.Now().Add(time.Hour)}
		s.WithdrawalGuard = &session.WithdrawalGuard{ID: "protected", Status: "unknown"}
	})
	a.openSupport(ctx, 42, 42, 0)
	state := a.sessions.Get(42)
	if state.Purchase != nil || state.Flow != "" || state.WithdrawalGuard == nil {
		t.Fatal("support bypassed guard or retained shopping input")
	}
	if a.handleSupportMessage(ctx, supportMessageFixture(42, 42, 20, "/products")) {
		t.Fatal("navigation was consumed")
	}
	if r, _, err := a.support.store.Get(ctx, support.ModeKey(42)); err != nil || r.Status != "closed" {
		t.Fatal("navigation did not leave support", err)
	}
}
func TestHumanSupportOrderContextChecksOwnership(t *testing.T) {
	a, f := newSupportFixture(t)
	ctx := context.Background()
	a.openSupport(ctx, 43, 43, 7)
	if _, found, _ := a.support.store.Get(ctx, support.ModeKey(43)); found {
		t.Fatal("foreign order activated support")
	}
	a.openSupport(ctx, 42, 42, 7)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.orders) != 2 || !strings.Contains(strings.Join(f.texts, "\n"), "SYNTHETIC-7") {
		t.Fatal("verified order context missing")
	}
}

type failingSupportStore struct {
	delegate support.Store
	all      bool
	routes   bool
}

func (s failingSupportStore) Get(ctx context.Context, key string) (support.Record, bool, error) {
	if s.all {
		return support.Record{}, false, support.ErrUnavailable
	}
	return s.delegate.Get(ctx, key)
}
func (s failingSupportStore) Update(ctx context.Context, key string, fn func(*support.Record) error) (support.Record, error) {
	if s.all || s.routes && strings.HasPrefix(key, "route:") {
		return support.Record{}, support.ErrUnavailable
	}
	return s.delegate.Update(ctx, key, fn)
}
func TestHumanSupportStorageFailureStopsForwardingAndUnmappedSendStaysUnknown(t *testing.T) {
	for _, all := range []bool{true, false} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			a, f := newSupportFixture(t)
			ctx := context.Background()
			a.openSupport(ctx, 42, 42, 0)
			a.support.store = failingSupportStore{delegate: a.support.store, all: all, routes: true}
			m := supportMessageFixture(42, 42, 10, "question")
			a.handleSupportMessage(ctx, m)
			a.handleSupportMessage(ctx, m)
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 1
			if all {
				want = 0
			}
			if len(f.copies) != want {
				t.Fatal("storage failure authorized another send", f.copies)
			}
			if !strings.Contains(strings.Join(f.texts, "\n"), "could not be confirmed") {
				t.Fatal("failure missing feedback")
			}
		})
	}
}

func TestHumanSupportOpenAndCloseCallbacksAcknowledgeBeforeWork(t *testing.T) {
	a, f := newSupportFixture(t)
	for _, data := range []string{"cs:open:0", "cs:close"} {
		q := &models.CallbackQuery{ID: "synthetic-" + data, From: models.User{ID: 42}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}}}}
		a.handleCallback(context.Background(), q)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.methods) != 4 || f.methods[0] != "answerCallbackQuery" || f.methods[1] != "sendMessage" || f.methods[2] != "answerCallbackQuery" || f.methods[3] != "sendMessage" {
		t.Fatalf("callbacks were not acknowledged once before work: %#v", f.methods)
	}
}
