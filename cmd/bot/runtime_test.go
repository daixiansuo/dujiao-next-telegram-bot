package main

import (
	"bytes"
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
	telegramapp "github.com/dunea/dujiao-next-telegram-bot/internal/telegram"
)

const runtimeTestToken = "999:synthetic-runtime-token"

type runtimeHeartbeat struct {
	Version    string   `json:"bot_version"`
	Connection string   `json:"webhook_status"`
	License    string   `json:"license_status"`
	Applied    *int     `json:"applied_config_version"`
	Warnings   []string `json:"warnings"`
}

type runtimeFixture struct {
	mu                                   sync.Mutex
	remote                               dujiao.BotConfigResponse
	public                               dujiao.TelegramPublicConfig
	failConfig, failPublic               bool
	publicReads, configReads, getMeReads int
	heartbeats                           []runtimeHeartbeat
	logs                                 bytes.Buffer
}

func newRuntimeFixture(t *testing.T) (*runtimeFixture, *dujiao.Client, *telegramapp.App, *slog.Logger) {
	t.Helper()
	fixture := &runtimeFixture{
		remote: dujiao.BotConfigResponse{ConfigVersion: 17, Config: dujiao.BotConfig{Enabled: true, BotToken: runtimeTestToken, DefaultLocale: "en-US"}},
		public: dujiao.TelegramPublicConfig{Enabled: true, BotUsername: "idid_runtime_test_bot", MiniAppURL: "https://shop.example.invalid/login"},
	}
	logger := slog.New(slog.NewTextHandler(&fixture.logs, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/bot"+runtimeTestToken+"/getMe":
			fixture.getMeReads++
			fmt.Fprint(w, `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Synthetic","username":"idid_runtime_test_bot"}}`)
		case r.URL.Path == "/api/v1/channel/telegram/config":
			fixture.configReads++
			if r.Method != http.MethodGet {
				t.Error("configuration method", r.Method)
			}
			if fixture.failConfig {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"status_code":503,"msg":"PRIVATE-CONFIG-ERROR"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": fixture.remote})
		case r.URL.Path == "/api/v1/public/config":
			fixture.publicReads++
			for name := range r.Header {
				if strings.HasPrefix(strings.ToLower(name), "dujiao-next-channel-") {
					t.Error("public request carried channel credentials", name)
				}
			}
			if fixture.failPublic {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"status_code":503,"msg":"PRIVATE-PUBLIC-ERROR"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": map[string]any{"telegram_auth": fixture.public}})
		case r.URL.Path == "/api/v1/channel/telegram/heartbeat":
			if r.Method != http.MethodPost {
				t.Error("heartbeat method", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			timestamp, err := strconv.ParseInt(r.Header.Get("Dujiao-Next-Channel-Timestamp"), 10, 64)
			if err != nil || r.Header.Get("Dujiao-Next-Channel-Key") != "runtime-key" || !dujiao.Verify("runtime-secret", http.MethodPost, r.URL.Path, timestamp, body, r.Header.Get("Dujiao-Next-Channel-Signature")) {
				t.Error("heartbeat signature invalid")
			}
			var heartbeat runtimeHeartbeat
			if err := json.Unmarshal(body, &heartbeat); err != nil {
				t.Error(err)
			}
			fixture.heartbeats = append(fixture.heartbeats, heartbeat)
			fmt.Fprint(w, `{"status_code":0,"data":{"config_version":17}}`)
		default:
			t.Errorf("unexpected request, polling or credential change: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	api, err := dujiao.NewClient(server.URL, "runtime-key", "runtime-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	initial := dujiao.BotConfig{Enabled: true, BotToken: runtimeTestToken, ConfigVersion: 3, PublicTelegram: dujiao.TelegramPublicConfig{Enabled: true, BotUsername: "idid_runtime_test_bot", MiniAppURL: "https://old.example.invalid/login"}}
	store := session.NewMemory("zh-CN")
	t.Cleanup(func() { _ = store.Close() })
	app, err := telegramapp.New(runtimeTestToken, api, initial, "zh-CN", time.Second, server.URL, logger, store)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, api, app, logger
}

func onlyRuntimeHeartbeat(t *testing.T, fixture *runtimeFixture) runtimeHeartbeat {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.heartbeats) != 1 {
		t.Fatalf("heartbeats=%d, want 1", len(fixture.heartbeats))
	}
	heartbeat := fixture.heartbeats[0]
	if heartbeat.Version != version || heartbeat.Connection != "long_polling" || heartbeat.License != "community" {
		t.Fatalf("unexpected runtime fields %#v", heartbeat)
	}
	return heartbeat
}

func TestSyncAndHeartbeatAppliesRemoteAndPublicConfiguration(t *testing.T) {
	for _, nestedVersion := range []int{0, 19} {
		t.Run(fmt.Sprint(nestedVersion), func(t *testing.T) {
			fixture, api, app, logger := newRuntimeFixture(t)
			fixture.mu.Lock()
			fixture.remote.Config.ConfigVersion = nestedVersion
			fixture.mu.Unlock()
			syncAndHeartbeat(context.Background(), logger, api, app)
			heartbeat := onlyRuntimeHeartbeat(t, fixture)
			wantVersion := 17
			if nestedVersion != 0 {
				wantVersion = nestedVersion
			}
			if heartbeat.Applied == nil || *heartbeat.Applied != wantVersion || len(heartbeat.Warnings) != 0 {
				t.Fatalf("applied configuration was not acknowledged correctly: %#v", heartbeat)
			}
			if got := app.PublicTelegramConfig(); got != fixture.public {
				t.Fatalf("public configuration not applied: %#v", got)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.configReads != 1 || fixture.publicReads != 1 || fixture.getMeReads != 2 {
				t.Fatalf("unexpected refresh/identity calls: config=%d public=%d getMe=%d", fixture.configReads, fixture.publicReads, fixture.getMeReads)
			}
		})
	}
}

func TestSyncAndHeartbeatConfigurationFailureStillReportsHeartbeat(t *testing.T) {
	fixture, api, app, logger := newRuntimeFixture(t)
	fixture.mu.Lock()
	fixture.failConfig = true
	fixture.mu.Unlock()
	previous := app.PublicTelegramConfig()
	syncAndHeartbeat(context.Background(), logger, api, app)
	heartbeat := onlyRuntimeHeartbeat(t, fixture)
	if heartbeat.Applied != nil || !runtimeHasWarning(heartbeat, "configuration_refresh_failed") {
		t.Fatalf("failed refresh acknowledged a version or lost warning: %#v", heartbeat)
	}
	if app.PublicTelegramConfig() != previous {
		t.Fatal("failed refresh replaced cached public configuration")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.configReads != 1 || fixture.publicReads != 0 || fixture.getMeReads != 1 {
		t.Fatalf("configuration failure unexpectedly continued refresh: config=%d public=%d getMe=%d", fixture.configReads, fixture.publicReads, fixture.getMeReads)
	}
	if strings.Contains(fixture.logs.String(), "PRIVATE-CONFIG-ERROR") {
		t.Fatal("remote error body leaked to log")
	}
}

func TestSyncAndHeartbeatPublicFailurePreservesCachedSettings(t *testing.T) {
	fixture, api, app, logger := newRuntimeFixture(t)
	fixture.mu.Lock()
	fixture.failPublic = true
	fixture.mu.Unlock()
	previous := app.PublicTelegramConfig()
	syncAndHeartbeat(context.Background(), logger, api, app)
	heartbeat := onlyRuntimeHeartbeat(t, fixture)
	if app.PublicTelegramConfig() != previous || !runtimeHasWarning(heartbeat, "public_configuration_refresh_failed") {
		t.Fatalf("public cache or failure diagnostic was lost: %#v", heartbeat)
	}
	// The Channel configuration itself was applied; the warning explicitly
	// describes the separate public settings still being served from cache.
	if heartbeat.Applied == nil || *heartbeat.Applied != 17 {
		t.Fatalf("valid Channel configuration not applied: %#v", heartbeat)
	}
	if strings.Contains(fixture.logs.String(), "PRIVATE-PUBLIC-ERROR") {
		t.Fatal("public error body leaked to log")
	}
}

func TestSyncAndHeartbeatTokenChangeRequiresRestartWithoutAcknowledgement(t *testing.T) {
	fixture, api, app, logger := newRuntimeFixture(t)
	fixture.mu.Lock()
	fixture.remote.Config.BotToken = "999:synthetic-rotated-token"
	fixture.mu.Unlock()
	syncAndHeartbeat(context.Background(), logger, api, app)
	heartbeat := onlyRuntimeHeartbeat(t, fixture)
	if heartbeat.Applied != nil || !runtimeHasWarning(heartbeat, "bot_token_changed_restart_required") {
		t.Fatalf("token change was incorrectly acknowledged: %#v", heartbeat)
	}
	if app.PublicTelegramConfig() != fixture.public {
		t.Fatal("token warning discarded valid public settings")
	}
	for _, secret := range []string{runtimeTestToken, "999:synthetic-rotated-token", "runtime-secret"} {
		if strings.Contains(fixture.logs.String(), secret) {
			t.Fatal("credential leaked into logs")
		}
	}
}

func runtimeHasWarning(heartbeat runtimeHeartbeat, want string) bool {
	for _, warning := range heartbeat.Warnings {
		if warning == want {
			return true
		}
	}
	return false
}
