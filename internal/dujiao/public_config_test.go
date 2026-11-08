package dujiao

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicTelegramConfigUsesPublicRouteWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/public/config" {
			t.Errorf("unexpected public request %s %s", r.Method, r.URL.Path)
		}
		for name := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), "dujiao-next-channel-") {
				t.Errorf("public request carried %s", name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status_code":0,"data":{"telegram_auth":{"enabled":true,"bot_username":"idid_test_bot","mini_app_url":"https://shop.example.invalid/login"},"unrelated":"ignored"}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "PRIVATE-KEY", "PRIVATE-SECRET", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PublicTelegramConfig(context.Background())
	if err != nil || !result.Enabled || result.BotUsername != "idid_test_bot" || result.MiniAppURL != "https://shop.example.invalid/login" {
		t.Fatalf("public settings = %#v, %v", result, err)
	}
}

func TestPublicTelegramConfigFailureDoesNotExposeResponse(t *testing.T) {
	for _, code := range []int{200, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, `{"status_code":503,"msg":"PRIVATE-RESPONSE"}`)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "key", "secret", time.Second)
			_, err := client.PublicTelegramConfig(context.Background())
			if err == nil || strings.Contains(err.Error(), "PRIVATE-RESPONSE") {
				t.Fatal("missing failure or exposed response", err)
			}
		})
	}
}
