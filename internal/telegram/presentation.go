package telegram

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func (a *App) welcomePresentation(ctx context.Context, chatID, userID int64, locale string) {
	state := a.sessions.Update(userID, func(s *session.State) { s.Locale = locale; s.Flow = "" })
	if !a.sessionOK(ctx, chatID, state.Err) {
		return
	}
	config := a.botConfig()
	name := strings.TrimSpace(config.Basic.DisplayName)
	if name == "" {
		name = "Dujiao Next"
	}
	text := "<b>" + html.EscapeString(truncate(name, 150)) + "</b>"
	if description := localized(config.Basic.Description, locale); description != "" {
		text += "\n" + html.EscapeString(truncate(description, 700))
	}
	if config.Welcome.Enabled {
		message := localized(config.Welcome.Message, locale)
		if message == "" {
			message = tr(locale, "欢迎来到商店，请选择功能。", "歡迎來到商店，請選擇功能。", "Welcome to the store. Choose an action.")
		}
		text += "\n\n" + html.EscapeString(truncate(message, 1800))
	} else {
		text += "\n\n" + tr(locale, "请选择功能：", "請選擇功能：", "Choose an action:")
	}
	markup := a.homePresentation(locale)
	if config.Welcome.Enabled && presentationURL(config.Basic.CoverURL, false) {
		// Caption limit is lower than message text. A long introduction is sent as a
		// separate message, retaining all navigation actions and readable text.
		caption := text
		var photoMarkup models.ReplyMarkup = markup
		if len([]rune(text)) > 900 {
			caption = "<b>" + html.EscapeString(truncate(name, 150)) + "</b>"
			photoMarkup = nil
		}
		_, err := a.bot.SendPhoto(ctx, &tgbot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: config.Basic.CoverURL}, Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: photoMarkup})
		if err == nil {
			if photoMarkup == nil {
				a.send(ctx, chatID, text, markup)
			}
			return
		}
		a.logger.Warn("welcome image unavailable")
	}
	a.send(ctx, chatID, text, markup)
}

func (a *App) helpPresentation(ctx context.Context, chatID int64, messageID int, locale string) {
	config := a.botConfig()
	title := tr(locale, "帮助中心", "說明中心", "Help center")
	intro := defaultHelpIntro(locale)
	if config.Help.Enabled {
		if value := localized(config.Help.Title, locale); value != "" {
			title = value
		}
		if value := localized(config.Help.Intro, locale); value != "" {
			intro = value
		}
	}
	text := "<b>" + html.EscapeString(truncate(title, 150)) + "</b>\n\n" + html.EscapeString(truncate(intro, 1800))
	var rows [][]models.InlineKeyboardButton
	if config.Help.Enabled {
		items := append([]dujiao.BotHelpItem(nil), config.Help.Items...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
		for _, item := range items {
			if !item.Enabled || strings.TrimSpace(item.Key) == "" {
				continue
			}
			label := localized(item.Summary, locale)
			if label == "" {
				label = localized(item.Title, locale)
			}
			if label == "" {
				continue
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(label, 80), CallbackData: "faq:" + presentationHash(item.Key)}})
		}
		if hint := localized(config.Help.CenterHint, locale); hint != "" {
			text += "\n\n" + html.EscapeString(truncate(hint, 500))
		}
	}
	support := a.supportEntryButtons(locale, 0)
	if len(support) > 0 {
		rows = append(rows, support)
	}
	if target := orderSupportTarget(config.Basic.SupportURL, ""); target.URL != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "💬 客服链接", "💬 客服連結", "💬 Support link"), URL: target.URL}})
	} else if len(support) == 0 && config.Help.Enabled {
		if hint := localized(config.Help.SupportHint, locale); hint != "" {
			text += "\n\n" + html.EscapeString(truncate(hint, 400))
		}
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: tr(locale, "📦 我的订单", "📦 我的訂單", "📦 Orders"), CallbackData: "orders:a:1"}, {Text: tr(locale, "💰 我的钱包", "💰 我的錢包", "💰 Wallet"), CallbackData: "wallet"}},
		[]models.InlineKeyboardButton{{Text: tr(locale, "🌐 切换语言", "🌐 切換語言", "🌐 Language"), CallbackData: "lang"}, {Text: tr(locale, "🔔 公告订阅", "🔔 公告訂閱", "🔔 Announcements"), CallbackData: "bcsubscriptions"}},
		shopHomeButtons(locale),
	)
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, messageID > 0)
}

func defaultHelpIntro(locale string) string {
	return tr(locale, "购买：选择商品和数量，确认订单后选择支付方式。\n订单与售后：在「我的订单」查看进度、下载交付资料或联系客服。\n充值与兑换：打开「我的钱包」。", "購買：選擇商品與數量，確認訂單後選擇付款方式。\n訂單與售後：在「我的訂單」查看進度、下載交付資料或聯絡客服。\n儲值與兌換：開啟「我的錢包」。", "Buying: choose a product and quantity, confirm the order, then choose a payment method.\nOrders and support: open My orders to check progress, download delivery details or contact support.\nRecharge and redemption: open My wallet.")
}

func (a *App) helpArticle(ctx context.Context, chatID int64, messageID int, locale, key string) {
	parts := strings.Split(key, ":")
	if len(parts) > 2 {
		a.presentationExpired(ctx, chatID, locale)
		return
	}
	page := 1
	if len(parts) == 2 {
		var err error
		page, err = strconv.Atoi(parts[1])
		if err != nil || page < 1 {
			a.presentationExpired(ctx, chatID, locale)
			return
		}
	}
	key = parts[0]
	config := a.botConfig()
	if config.Help.Enabled {
		for _, item := range config.Help.Items {
			if !item.Enabled || strings.TrimSpace(item.Key) == "" || presentationHash(item.Key) != key {
				continue
			}
			title := localized(item.Title, locale)
			if title == "" {
				title = localized(item.Summary, locale)
			}
			content := []rune(localized(item.Content, locale))
			const articlePageSize = 2800
			totalPages := max(1, (len(content)+articlePageSize-1)/articlePageSize)
			if page > totalPages {
				a.presentationExpired(ctx, chatID, locale)
				return
			}
			start := (page - 1) * articlePageSize
			text := "<b>" + html.EscapeString(truncate(title, 150)) + "</b>"
			if totalPages > 1 {
				text += fmt.Sprintf(" · %d/%d", page, totalPages)
			}
			text += "\n\n" + html.EscapeString(string(content[start:min(len(content), start+articlePageSize)]))
			var rows [][]models.InlineKeyboardButton
			var navigation []models.InlineKeyboardButton
			if page > 1 {
				navigation = append(navigation, models.InlineKeyboardButton{Text: tr(locale, "‹ 上一页", "‹ 上一頁", "‹ Previous"), CallbackData: fmt.Sprintf("faq:%s:%d", key, page-1)})
			}
			if page < totalPages {
				navigation = append(navigation, models.InlineKeyboardButton{Text: tr(locale, "下一页 ›", "下一頁 ›", "Next ›"), CallbackData: fmt.Sprintf("faq:%s:%d", key, page+1)})
			}
			if len(navigation) > 0 {
				rows = append(rows, navigation)
			}
			if item.ShowSupportLink {
				support := a.supportEntryButtons(locale, 0)
				if len(support) > 0 {
					rows = append(rows, support)
				}
				if target := orderSupportTarget(config.Basic.SupportURL, ""); target.URL != "" {
					rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "💬 客服链接", "💬 客服連結", "💬 Support link"), URL: target.URL}})
				} else if len(support) == 0 {
					if hint := localized(config.Help.SupportHint, locale); hint != "" {
						text += "\n\n" + html.EscapeString(truncate(hint, 400))
					}
				}
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 帮助中心", "‹ 說明中心", "‹ Help"), CallbackData: "help"}, {Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"}})
			a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, messageID > 0)
			return
		}
	}
	a.presentationExpired(ctx, chatID, locale)
}

func (a *App) homePresentation(locale string) models.InlineKeyboardMarkup {
	config := a.botConfig()
	if len(config.Menu.Items) == 0 {
		config.Menu.Items = []dujiao.BotMenuItem{
			{Key: "shop_home", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "🛍 浏览商品", "🛍 瀏覽商品", "🛍 Browse products")}},
			{Key: "my_orders", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "📦 我的订单", "📦 我的訂單", "📦 My orders")}},
			{Key: "my_wallet", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "💰 余额", "💰 餘額", "💰 Wallet")}},
			{Key: "affiliate", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "🤝 推广中心", "🤝 推廣中心", "🤝 Affiliate")}},
			{Key: "gift_card", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "🎁 礼品卡兑换", "🎁 禮品卡兌換", "🎁 Redeem card")}},
			{Key: "switch_language", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "🌐 语言", "🌐 語言", "🌐 Language")}},
			{Key: "contact_support", Enabled: true, Label: dujiao.LocalizedText{locale: tr(locale, "❓ 帮助", "❓ 幫助", "❓ Help")}},
		}
		// No merchant menu exists to authorize hashes against; default navigation
		// uses the established callbacks, which remain reachable from business pages.
		var buttons []models.InlineKeyboardButton
		for _, item := range config.Menu.Items {
			buttons = append(buttons, models.InlineKeyboardButton{Text: localized(item.Label, locale), CallbackData: presentationBuiltinCallback(item.Key)})
		}
		buttons = append(buttons, models.InlineKeyboardButton{Text: tr(locale, "🔔 公告订阅", "🔔 公告訂閱", "🔔 Announcements"), CallbackData: "bcsubscriptions"})
		return models.InlineKeyboardMarkup{InlineKeyboard: compactActionRows(buttons, "cats")}
	}
	rows := a.configuredPresentationRows(config.Menu.Items, locale)
	var buttons []models.InlineKeyboardButton
	primary := ""
	for _, row := range rows {
		for _, button := range row {
			buttons = append(buttons, button)
		}
	}
	for _, item := range config.Menu.Items {
		if item.Enabled && presentationBuiltinKey(item) == "shop_home" && (item.Action.Type == "builtin" || item.Action.Type == "") {
			primary = "cfg:" + menuActionHash(item)
			break
		}
	}
	buttons = append(buttons, models.InlineKeyboardButton{Text: tr(locale, "🔔 公告订阅", "🔔 公告訂閱", "🔔 Announcements"), CallbackData: "bcsubscriptions"})
	return models.InlineKeyboardMarkup{InlineKeyboard: compactActionRows(buttons, primary)}
}

func (a *App) configuredPresentationRows(items []dujiao.BotMenuItem, locale string) [][]models.InlineKeyboardButton {
	items = append([]dujiao.BotMenuItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	rows := make([][]models.InlineKeyboardButton, 0, len(items))
	for _, item := range items {
		label := localized(item.Label, locale)
		if !item.Enabled || label == "" {
			continue
		}
		button := models.InlineKeyboardButton{Text: truncate(label, 100)}
		switch item.Action.Type {
		case "", "builtin":
			if presentationBuiltinCallback(presentationBuiltinKey(item)) == "" {
				continue
			}
			button.CallbackData = "cfg:" + menuActionHash(item)
		case "url":
			if !presentationURL(item.Action.Value, false) {
				continue
			}
			button.URL = strings.TrimSpace(item.Action.Value)
		case "web_app":
			target := strings.TrimSpace(item.Action.Value)
			if target == "" {
				target = a.botConfig().PublicTelegram.MiniAppURL
			}
			if !presentationURL(target, true) {
				continue
			}
			button.WebApp = &models.WebAppInfo{URL: target}
		case "command":
			if _, ok := presentationCommand(item.Action.Value); !ok {
				continue
			}
			button.CallbackData = "cfg:" + menuActionHash(item)
		default:
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{button})
	}
	return rows
}

func presentationBuiltinKey(item dujiao.BotMenuItem) string {
	if value := strings.TrimSpace(item.Action.Value); value != "" {
		return value
	}
	return item.Key
}
func presentationBuiltinCallback(key string) string {
	return map[string]string{"shop_home": "cats", "my_orders": "orders:1", "my_wallet": "wallet", "affiliate": "affiliate", "my_affiliate": "affiliate", "gift_card": "giftcard", "switch_language": "lang", "contact_support": "help"}[key]
}
func presentationBuiltinCommand(key string) string {
	return map[string]string{"shop_home": "/products", "my_orders": "/orders", "my_wallet": "/wallet", "affiliate": "/affiliate", "my_affiliate": "/affiliate", "gift_card": "/redeem", "switch_language": "/language", "contact_support": "/help"}[key]
}
func menuActionHash(item dujiao.BotMenuItem) string {
	return presentationHash(item.Key + "\x00" + item.Action.Type + "\x00" + item.Action.Value)
}
func presentationHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:12])
}
func presentationURL(raw string, httpsOnly bool) bool {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 2048 || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	target, err := url.Parse(value)
	return err == nil && target.Hostname() != "" && target.User == nil && (target.Scheme == "https" || (!httpsOnly && target.Scheme == "http"))
}
func presentationCommand(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\t") {
		return "", false
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	parts := strings.Fields(value)
	if len(parts) > 2 || strings.Contains(parts[0], "@") {
		return "", false
	}
	command, _ := parseCommand(value)
	switch command {
	case "/start", "/products", "/orders", "/wallet", "/recharge", "/redeem", "/affiliate", "/language", "/help", "/whoami", "/cancel", "/support", "/subscribe", "/unsubscribe":
		return value, true
	}
	return "", false
}

func (a *App) handleConfiguredMenu(ctx context.Context, query *models.CallbackQuery) bool {
	if !strings.HasPrefix(query.Data, "cfg:") && !strings.HasPrefix(query.Data, "faq:") {
		return false
	}
	message := query.Message.Message
	if message == nil || message.Chat.ID != query.From.ID || query.From.ID <= 0 {
		return true
	}
	state := a.sessions.Get(query.From.ID)
	if !a.sessionOK(ctx, message.Chat.ID, state.Err) {
		return true
	}
	if strings.HasPrefix(query.Data, "faq:") {
		a.helpArticle(ctx, message.Chat.ID, message.ID, state.Locale, strings.TrimPrefix(query.Data, "faq:"))
		return true
	}
	hash := strings.TrimPrefix(query.Data, "cfg:")
	for _, item := range a.botConfig().Menu.Items {
		if !item.Enabled || menuActionHash(item) != hash {
			continue
		}
		command := ""
		switch item.Action.Type {
		case "", "builtin":
			command = presentationBuiltinCommand(presentationBuiltinKey(item))
		case "command":
			command, _ = presentationCommand(item.Action.Value)
		}
		if command == "" {
			break
		}
		// Reuse the same command handlers and their ownership/financial checks. No
		// merchant-provided callback or arbitrary operation is executed.
		user := query.From
		a.handleUpdate(ctx, a.bot, &models.Update{Message: &models.Message{ID: message.ID, From: &user, Chat: message.Chat, Text: command}})
		return true
	}
	a.presentationExpired(ctx, message.Chat.ID, state.Locale)
	return true
}
func (a *App) presentationExpired(ctx context.Context, chatID int64, locale string) {
	a.send(ctx, chatID, tr(locale, "这个入口已关闭或配置已更新，请返回首页重新选择。", "這個入口已關閉或設定已更新，請返回首頁重新選擇。", "This action is disabled or its configuration changed. Return to the home menu."), a.backKeyboard(locale))
}
