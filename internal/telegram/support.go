package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/support"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type supportService struct {
	groupID int64
	staff   map[int64]bool
	store   support.Store
}

// ConfigureSupport is startup-only. Production configuration requires Redis;
// the in-memory provider remains useful for isolated interaction tests.
func (a *App) ConfigureSupport(groupID int64, staffIDs []int64) error {
	if groupID >= 0 || len(staffIDs) == 0 || len(staffIDs) > 10 {
		return errors.New("support requires a group and 1–10 staff IDs")
	}
	staff := make(map[int64]bool, len(staffIDs))
	for _, id := range staffIDs {
		if id <= 0 {
			return errors.New("invalid support staff ID")
		}
		staff[id] = true
	}
	provider, ok := a.sessions.(interface{ SupportStore() support.Store })
	if !ok {
		return errors.New("session provider does not support customer routing")
	}
	a.support = &supportService{groupID: groupID, staff: staff, store: provider.SupportStore()}
	return nil
}

func (a *App) supportEntryButtons(locale string, orderID uint) []models.InlineKeyboardButton {
	if a.support == nil {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: tr(locale, "💬 人工客服", "💬 人工客服", "💬 Human support"), CallbackData: "cs:open:" + strconv.FormatUint(uint64(orderID), 10)}}
}
func (a *App) supportExitKeyboard(locale string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "返回商店", "返回商店", "Return to store"), CallbackData: "cs:close"}}}}
}
func (a *App) supportUnavailable(ctx context.Context, chatID int64, locale string) {
	a.logger.Warn("support action not completed")
	a.send(ctx, chatID, tr(locale, "客服消息暂时无法确认发送，请稍后核对，不要连续重复发送。", "客服訊息暫時無法確認傳送，請稍後核對，不要連續重複傳送。", "The support message could not be confirmed. Check again later; avoid repeated sends."), nil)
}

func (a *App) openSupport(ctx context.Context, chatID, userID int64, orderID uint) {
	if chatID != userID || userID <= 0 {
		return
	}
	state := a.sessions.Get(userID)
	if !a.sessionOK(ctx, chatID, state.Err) {
		return
	}
	locale := state.Locale
	if a.support == nil {
		a.send(ctx, chatID, tr(locale, "人工客服暂未启用，请使用帮助中心的联系方式。", "人工客服暫未啟用，請使用說明中心的聯絡方式。", "Human support is not enabled. Use the help center's contact details."), nil)
		return
	}
	var orderText string
	summaryFailed := false
	if orderID > 0 {
		order, err := a.api.Order(ctx, strconv.FormatInt(userID, 10), locale, orderID)
		if err != nil || order.OrderID != orderID {
			a.send(ctx, chatID, tr(locale, "无法核实此订单，请从自己的订单页重新进入客服。", "無法核實此訂單，請從自己的訂單頁重新進入客服。", "The order could not be verified. Open support from your own order page."), nil)
			return
		}
		orderText = tr(locale, "订单号：", "訂單號：", "Order: ") + order.OrderNo + "\n" + tr(locale, "状态：", "狀態：", "Status: ") + orderStatus(locale, order.Status)
	}
	if !a.sessionOK(ctx, chatID, a.sessions.ResetFlow(userID)) {
		return
	}
	_, err := a.support.store.Update(ctx, support.ModeKey(userID), func(r *support.Record) error {
		*r = support.Record{Kind: "mode", UserID: userID, OrderID: orderID, Status: "active", ExpiresAt: time.Now().Add(support.ModeLifetime)}
		return nil
	})
	if err != nil {
		a.supportUnavailable(ctx, chatID, locale)
		return
	}
	// The verified summary contains no cards or payment credentials. A failed
	// summary does not authorize guessing a reply target: only saved routes do.
	if orderText != "" {
		header, sendErr := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: a.support.groupID, Text: fmt.Sprintf(tr(locale, "客户 #%d\n", "客戶 #%d\n", "Customer #%d\n"), userID) + orderText})
		if sendErr == nil && header != nil && header.ID > 0 {
			_, sendErr = a.support.store.Update(ctx, support.RouteKey(a.support.groupID, header.ID), func(r *support.Record) error {
				*r = support.Record{Kind: "route", UserID: userID, SourceMessageID: header.ID, OrderID: orderID, ExpiresAt: time.Now().Add(support.MessageLifetime)}
				return nil
			})
		}
		if sendErr != nil || header == nil || header.ID <= 0 {
			summaryFailed = true
			a.logger.Warn("support order summary not confirmed")
		}
	}
	text := tr(locale, "已进入人工客服。请发送问题描述、图片或文件；点击「返回商店」结束客服输入。", "已進入人工客服。請傳送問題描述、圖片或檔案；點擊「返回商店」結束客服輸入。", "Human support is open. Send your question, photos or files. Tap Return to store to leave support mode.")
	if orderText != "" {
		text += "\n\n" + html.EscapeString(orderText)
	}
	if summaryFailed {
		text += "\n\n" + tr(locale, "订单摘要转交未获确认，请在咨询中附上订单号。", "訂單摘要轉交未獲確認，請在諮詢中附上訂單號。", "The order summary was not confirmed. Include your order number in the enquiry.")
	}
	a.send(ctx, chatID, text, a.supportExitKeyboard(locale))
}
func (a *App) stopSupport(ctx context.Context, userID int64) error {
	if a.support == nil || userID <= 0 {
		return nil
	}
	_, found, err := a.support.store.Get(ctx, support.ModeKey(userID))
	if err != nil || !found {
		return err
	}
	_, err = a.support.store.Update(ctx, support.ModeKey(userID), func(r *support.Record) error { r.Status = "closed"; return nil })
	return err
}
func (a *App) handleSupportCallback(ctx context.Context, q *models.CallbackQuery) bool {
	if !strings.HasPrefix(q.Data, "cs:") {
		return false
	}
	m := q.Message.Message
	if m == nil {
		return true
	}
	if q.Data == "cs:noop" {
		if a.support != nil && m.Chat.ID == a.support.groupID && a.support.staff[q.From.ID] {
			_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: tr(a.sessions.Get(q.From.ID).Locale, "请回复这条消息处理客户咨询。", "請回覆這則訊息處理客戶諮詢。", "Reply to this message to answer the customer.")})
		}
		return true
	}
	if m.Chat.ID != q.From.ID || q.From.ID <= 0 {
		return true
	}
	// Acknowledge before storage or network work so the client does not keep
	// spinning while opening/closing a conversation.
	_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if q.Data == "cs:close" {
		if err := a.stopSupport(ctx, q.From.ID); err != nil {
			a.supportUnavailable(ctx, m.Chat.ID, a.sessions.Get(q.From.ID).Locale)
			return true
		}
		locale := a.sessions.Get(q.From.ID).Locale
		a.send(ctx, m.Chat.ID, tr(locale, "已返回商店；已发送咨询的客服回复仍会送达。", "已返回商店；已傳送諮詢的客服回覆仍會送達。", "Back in the store. Replies to your existing enquiry may still arrive."), a.homeKeyboard(locale))
		return true
	}
	p := strings.Split(q.Data, ":")
	if len(p) != 3 || p[1] != "open" {
		return true
	}
	id, err := strconv.ParseUint(p[2], 10, 64)
	if err != nil || uint64(uint(id)) != id {
		return true
	}
	a.openSupport(ctx, m.Chat.ID, q.From.ID, uint(id))
	return true
}
func supportContent(m *models.Message) bool {
	return m.Text != "" || len(m.Photo) > 0 || m.Document != nil || m.Voice != nil || m.Audio != nil || m.Video != nil || m.VideoNote != nil || m.Animation != nil || m.Sticker != nil
}
func supportNavigation(command string) bool {
	switch command {
	case "/start", "/products", "/orders", "/wallet", "/recharge", "/redeem", "/affiliate", "/language", "/help", "/cancel", "/back", "/subscribe", "/unsubscribe", "/broadcast", "/broadcast_status", "/admin", "/whoami":
		return true
	}
	return false
}
func (a *App) handleSupportMessage(ctx context.Context, m *models.Message) bool {
	if a.support == nil || m == nil || m.From == nil {
		return false
	}
	if m.Chat.ID == a.support.groupID {
		if m.From.IsBot || m.SenderChat != nil || !a.support.staff[m.From.ID] || m.ReplyToMessage == nil {
			return true
		}
		route, found, err := a.support.store.Get(ctx, support.RouteKey(m.Chat.ID, m.ReplyToMessage.ID))
		if err != nil {
			a.supportUnavailable(ctx, m.Chat.ID, a.sessions.Get(m.From.ID).Locale)
			return true
		}
		if !found {
			a.send(ctx, m.Chat.ID, tr(a.sessions.Get(m.From.ID).Locale, "此消息没有有效客户映射或已超过 7 天，请回复新的客户咨询。", "此訊息沒有有效客戶對應或已超過 7 天，請回覆新的客戶諮詢。", "No valid customer route (or it expired after 7 days). Reply to a recent enquiry."), nil)
			return true
		}
		if !supportContent(m) {
			return true
		}
		a.relaySupport(ctx, m, route.UserID, support.DeliveryKey("staff", m.Chat.ID, m.ID), route.UserID, route.OrderID, false)
		return true
	}
	if m.Chat.ID != m.From.ID || m.From.ID <= 0 {
		return false
	}
	command, _ := parseCommand(m.Text)
	if command == "/support" {
		a.openSupport(ctx, m.Chat.ID, m.From.ID, 0)
		return true
	}
	mode, found, err := a.support.store.Get(ctx, support.ModeKey(m.From.ID))
	if err != nil {
		a.supportUnavailable(ctx, m.Chat.ID, a.sessions.Get(m.From.ID).Locale)
		return true
	}
	if !found || mode.Status != "active" {
		return false
	}
	if supportNavigation(command) {
		if err := a.stopSupport(ctx, m.From.ID); err != nil {
			a.supportUnavailable(ctx, m.Chat.ID, a.sessions.Get(m.From.ID).Locale)
			return true
		}
		return false
	}
	if !supportContent(m) {
		return true
	}
	// Refresh the idle expiry while preserving the verified order context.
	_, err = a.support.store.Update(ctx, support.ModeKey(m.From.ID), func(r *support.Record) error {
		if r.Status != "active" {
			return support.ErrConflict
		}
		r.ExpiresAt = time.Now().Add(support.ModeLifetime)
		return nil
	})
	if err != nil {
		a.supportUnavailable(ctx, m.Chat.ID, a.sessions.Get(m.From.ID).Locale)
		return true
	}
	a.relaySupport(ctx, m, a.support.groupID, support.DeliveryKey("customer", m.Chat.ID, m.ID), m.From.ID, mode.OrderID, true)
	return true
}
func (a *App) relaySupport(ctx context.Context, m *models.Message, target int64, key string, userID int64, orderID uint, customer bool) {
	token := rand.Text()
	_, err := a.support.store.Update(ctx, key, func(r *support.Record) error {
		if r.Status != "" && r.Status != "failed" {
			return support.ErrDuplicate
		}
		*r = support.Record{Kind: "delivery", UserID: userID, SourceMessageID: m.ID, OrderID: orderID, Status: "posting", Token: token, ExpiresAt: time.Now().Add(support.MessageLifetime)}
		return nil
	})
	if errors.Is(err, support.ErrDuplicate) {
		return
	}
	state := a.sessions.Get(userID)
	locale := state.Locale
	if state.Err != nil {
		a.supportUnavailable(ctx, m.Chat.ID, locale)
		return
	}
	if err != nil {
		a.supportUnavailable(ctx, m.Chat.ID, locale)
		return
	}
	params := &tgbot.CopyMessageParams{ChatID: target, FromChatID: m.Chat.ID, MessageID: m.ID}
	if customer {
		label := fmt.Sprintf(tr(locale, "客户 #%d", "客戶 #%d", "Customer #%d"), userID)
		if orderID > 0 {
			label += fmt.Sprintf(tr(locale, " · 订单 #%d", " · 訂單 #%d", " · Order #%d"), orderID)
		}
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: label, CallbackData: "cs:noop"}}}}
	}
	copied, sendErr := a.bot.CopyMessage(ctx, params)
	status := "sent"
	if sendErr != nil || copied == nil || copied.ID <= 0 {
		status = "unknown"
		var flood *tgbot.TooManyRequestsError
		if errors.As(sendErr, &flood) || errors.Is(sendErr, tgbot.ErrorBadRequest) || errors.Is(sendErr, tgbot.ErrorForbidden) || errors.Is(sendErr, tgbot.ErrorNotFound) {
			status = "failed"
		}
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if status == "sent" && customer {
		_, err = a.support.store.Update(finish, support.RouteKey(target, copied.ID), func(r *support.Record) error {
			*r = support.Record{Kind: "route", UserID: userID, SourceMessageID: m.ID, OrderID: orderID, ExpiresAt: time.Now().Add(support.MessageLifetime)}
			return nil
		})
		if err != nil {
			status = "unknown"
		}
	}
	_, saveErr := a.support.store.Update(finish, key, func(r *support.Record) error {
		if r.Token != token || r.Status != "posting" {
			return support.ErrConflict
		}
		r.Status = status
		if copied != nil {
			r.TelegramMessageID = copied.ID
		}
		return nil
	})
	if saveErr != nil || status == "unknown" {
		a.supportUnavailable(ctx, m.Chat.ID, locale)
		return
	}
	if status == "failed" {
		a.send(ctx, m.Chat.ID, tr(locale, "客服消息未发送成功，请检查对方是否可接收，稍后重新发送。", "客服訊息未傳送成功，請檢查對方是否可接收，稍後重新傳送。", "The support message was not delivered. Check availability and send it again later."), nil)
		return
	}
	if !customer {
		a.send(ctx, m.Chat.ID, tr(a.sessions.Get(m.From.ID).Locale, "已回复客户。", "已回覆客戶。", "Reply delivered."), nil)
	}
}
