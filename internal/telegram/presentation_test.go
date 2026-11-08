package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/go-telegram/bot/models"
)

func presentationMenuItem(key, action, value string) dujiao.BotMenuItem {
	return dujiao.BotMenuItem{Key: key, Enabled: true, Label: dujiao.LocalizedText{"zh-CN": key, "zh-TW": key, "en-US": key}, Action: dujiao.BotMenuAction{Type: action, Value: value}}
}

func TestPresentationMenuSupportsBuiltinURLWebAppAndCommands(t *testing.T) {
	app := &App{}
	app.UpdateConfig(dujiao.BotConfig{PublicTelegram: dujiao.TelegramPublicConfig{MiniAppURL: "https://shop.example.invalid/login"}})
	var items []dujiao.BotMenuItem
	for _, key := range []string{"shop_home", "my_orders", "my_wallet", "affiliate", "gift_card", "switch_language", "contact_support"} {
		items = append(items, presentationMenuItem(key, "builtin", ""))
	}
	items = append(items, presentationMenuItem("docs", "url", "https://shop.example.invalid/"+strings.Repeat("a", 200)), presentationMenuItem("app", "web_app", ""), presentationMenuItem("command", "command", "orders SYNTHETIC-ORDER"))
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		rows := app.configuredPresentationRows(items, locale)
		if len(rows) != 10 {
			t.Fatalf("%s: missing configured actions: %#v", locale, rows)
		}
		for i := 0; i < 7; i++ {
			if rows[i][0].CallbackData != "cfg:"+menuActionHash(items[i]) {
				t.Fatalf("missing builtin %s", items[i].Key)
			}
		}
		if rows[7][0].URL != items[7].Action.Value || rows[7][0].CallbackData != "" {
			t.Fatal("long URL was encoded as a callback")
		}
		if rows[8][0].WebApp == nil || rows[8][0].WebApp.URL != "https://shop.example.invalid/login" || rows[8][0].URL != "" || rows[8][0].CallbackData != "" {
			t.Fatal("missing real WebApp button", rows[8])
		}
		for _, row := range rows {
			if len(row[0].CallbackData) > 64 {
				t.Fatal("callback exceeds Telegram limit")
			}
		}
	}
	override := presentationMenuItem("renamed", "builtin", "gift_card")
	if got := app.configuredPresentationRows([]dujiao.BotMenuItem{override}, "en-US"); len(got) != 1 || got[0][0].CallbackData != "cfg:"+menuActionHash(override) {
		t.Fatal("builtin value was not honored")
	}
}

func TestPresentationMenuRejectsUnsafeOrUnsupportedActions(t *testing.T) {
	app := &App{}
	disabled := presentationMenuItem("disabled", "builtin", "my_wallet")
	disabled.Enabled = false
	items := []dujiao.BotMenuItem{disabled, presentationMenuItem("js", "url", "javascript:alert(1)"), presentationMenuItem("credentials", "url", "https://secret@example.invalid/"), presentationMenuItem("httpapp", "web_app", "http://example.invalid/"), presentationMenuItem("admin", "command", "broadcast"), presentationMenuItem("injection", "command", "orders\n/wallet"), presentationMenuItem("unknown", "builtin", "unknown")}
	if got := app.configuredPresentationRows(items, "en-US"); len(got) != 0 {
		t.Fatal("invalid menu actions rendered", got)
	}
	for _, value := range []string{"https://example.invalid/", "http://example.invalid/"} {
		if !presentationURL(value, false) {
			t.Fatal("valid URL rejected")
		}
	}
}

func TestPresentationConfiguredCallbackChecksLatestSettings(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	original := presentationMenuItem("balance", "builtin", "my_wallet")
	query := &models.CallbackQuery{ID: "synthetic", From: models.User{ID: 42}, Data: "cfg:" + menuActionHash(original), Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}}}}
	for _, changed := range []dujiao.BotMenuItem{
		{Key: original.Key, Enabled: false, Action: original.Action, Label: original.Label},
		presentationMenuItem(original.Key, "builtin", "gift_card"),
	} {
		app.UpdateConfig(dujiao.BotConfig{Enabled: true, Menu: dujiao.BotMenuConfig{Items: []dujiao.BotMenuItem{changed}}})
		if !app.handleConfiguredMenu(context.Background(), query) {
			t.Fatal("configured callback not claimed")
		}
	}
	fixture.mu.Lock()
	text := strings.Join(fixture.texts, "\n")
	fixture.mu.Unlock()
	if strings.Contains(text, "150.00") || len(fixture.giftCodes) > 0 {
		t.Fatal("stale menu executed business action")
	}
	if !strings.Contains(text, "入口已关闭") {
		t.Fatal("stale action had no feedback")
	}
	app.UpdateConfig(dujiao.BotConfig{Enabled: true, Menu: dujiao.BotMenuConfig{Items: []dujiao.BotMenuItem{original}}})
	app.handleConfiguredMenu(context.Background(), query)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !strings.Contains(strings.Join(fixture.texts, "\n"), "150.00") {
		t.Fatal("current builtin failed to use existing command handler")
	}
}

func TestPresentationHelpUsesArticleContractAndRevalidatesOldButtons(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	item := dujiao.BotHelpItem{Key: "orders", Enabled: true, Summary: dujiao.LocalizedText{"zh-CN": "订单常见问题"}, Title: dujiao.LocalizedText{"zh-CN": "订单说明"}, Content: dujiao.LocalizedText{"zh-CN": "<PRIVATE-ARTICLE>"}, ShowSupportLink: true}
	cfg := dujiao.BotConfig{Enabled: true, Help: dujiao.BotHelpConfig{Enabled: true, Title: dujiao.LocalizedText{"zh-CN": "自定义帮助"}, Intro: dujiao.LocalizedText{"zh-CN": "自定义介绍"}, CenterHint: dujiao.LocalizedText{"zh-CN": "请选择问题"}, SupportHint: dujiao.LocalizedText{"zh-CN": "请稍后联系"}, Items: []dujiao.BotHelpItem{item}}}
	app.UpdateConfig(cfg)
	app.helpPresentation(context.Background(), 42, 0, "zh-CN")
	app.helpArticle(context.Background(), 42, 10, "zh-CN", presentationHash(item.Key))
	fixture.mu.Lock()
	text := strings.Join(fixture.texts, "\n")
	markup := strings.Join(fixture.markups, "\n")
	fixture.mu.Unlock()
	if !strings.Contains(text, "自定义帮助") || !strings.Contains(text, "请选择问题") || !strings.Contains(text, "&lt;PRIVATE-ARTICLE&gt;") || !strings.Contains(markup, "faq:"+presentationHash(item.Key)) {
		t.Fatal("incomplete custom help rendering", text, markup)
	}
	cfg.Help.Enabled = false
	app.UpdateConfig(cfg)
	fixture.mu.Lock()
	fixture.texts = nil
	fixture.mu.Unlock()
	app.helpArticle(context.Background(), 42, 10, "zh-CN", presentationHash(item.Key))
	app.helpPresentation(context.Background(), 42, 0, "zh-CN")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	text = strings.Join(fixture.texts, "\n")
	if strings.Contains(text, "PRIVATE-ARTICLE") || strings.Contains(text, "自定义介绍") || !strings.Contains(text, "购买：") {
		t.Fatal("disabled custom help did not fall back", text)
	}
}

func TestPresentationWelcomeRespectsSwitchAndFallsBackFromImage(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	cfg := dujiao.BotConfig{Enabled: true, Basic: dujiao.BotBasicConfig{DisplayName: "IDID", Description: dujiao.LocalizedText{"zh-CN": "平台介绍"}, CoverURL: "https://example.invalid/cover.png"}, Welcome: dujiao.BotWelcomeConfig{Enabled: false, Message: dujiao.LocalizedText{"zh-CN": "CUSTOM-WELCOME"}}}
	app.UpdateConfig(cfg)
	app.welcomePresentation(context.Background(), 42, 42, "zh-CN")
	fixture.mu.Lock()
	text := strings.Join(fixture.texts, "\n")
	fixture.texts = nil
	fixture.mu.Unlock()
	if strings.Contains(text, "CUSTOM-WELCOME") || !strings.Contains(text, "IDID") || !strings.Contains(text, "平台介绍") {
		t.Fatal("welcome switch or introduction ignored", text)
	}
	cfg.Welcome.Enabled = true
	app.UpdateConfig(cfg)
	// This fixture returns a non-message photo result, exercising text fallback.
	app.welcomePresentation(context.Background(), 42, 42, "zh-CN")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !strings.Contains(strings.Join(fixture.texts, "\n"), "CUSTOM-WELCOME") {
		t.Fatal("image failure lost welcome text")
	}
}

func TestPresentationHelpConfigDecodesAllMallFields(t *testing.T) {
	var config dujiao.BotConfig
	err := json.Unmarshal([]byte(`{"help":{"enabled":true,"center_hint":{"en-US":"Choose"},"support_hint":{"en-US":"Offline"},"items":[{"key":"orders","enabled":true,"order":2,"summary":{"en-US":"Orders"},"title":{"en-US":"Order help"},"content":{"en-US":"Content"},"show_support_link":true}]}}`), &config)
	if err != nil || config.Help.CenterHint["en-US"] != "Choose" || len(config.Help.Items) != 1 || !config.Help.Items[0].ShowSupportLink || config.Help.Items[0].Content["en-US"] != "Content" {
		t.Fatalf("incomplete help contract %#v, %v", config.Help, err)
	}
}

func TestPresentationLongHelpArticleKeepsFullContentAcrossPages(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	item := dujiao.BotHelpItem{Key: "long", Enabled: true, Title: dujiao.LocalizedText{"zh-CN": "长说明"}, Content: dujiao.LocalizedText{"zh-CN": strings.Repeat("中", 2800) + "结尾内容"}}
	app.UpdateConfig(dujiao.BotConfig{Enabled: true, Help: dujiao.BotHelpConfig{Enabled: true, Items: []dujiao.BotHelpItem{item}}})
	key := presentationHash(item.Key)
	app.helpArticle(context.Background(), 42, 10, "zh-CN", key)
	app.helpArticle(context.Background(), 42, 10, "zh-CN", key+":2")
	app.helpArticle(context.Background(), 42, 10, "zh-CN", key+":999999999999999999999999")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !strings.Contains(fixture.texts[0], "1/2") || !strings.Contains(fixture.markups[0], "faq:"+key+":2") || !strings.Contains(fixture.texts[1], "结尾内容") || !strings.Contains(fixture.texts[2], "入口已关闭") {
		t.Fatal("long FAQ pagination lost content or accepted an invalid page")
	}
}
