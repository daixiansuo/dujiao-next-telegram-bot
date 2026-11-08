package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var errOrderUnconfirmed = errors.New("previous order creation is unconfirmed")

func orderCreationUnknown(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *dujiao.APIError
	return !errors.As(err, &apiErr) || apiErr.HTTPStatus < 400 || apiErr.HTTPStatus >= 500 || apiErr.HTTPStatus == 408 || apiErr.HTTPStatus == 429
}

func orderUnknownText(locale string) string {
	return tr(locale,
		"暂时无法确认上一笔订单是否创建成功，请勿重复下单。请查看订单记录并核对，必要时联系人工客服。",
		"暫時無法確認上一筆訂單是否建立成功，請勿重複下單。請查看訂單記錄並核對，必要時聯絡人工客服。",
		"The previous order creation result is unknown. Do not place it again. Check your orders and contact support if needed.")
}

func orderGuardKeyboard(locale string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: tr(locale, "查看订单核对", "查看訂單核對", "Review orders"), CallbackData: "orders:a:1"},
		{Text: tr(locale, "帮助 / 客服", "說明 / 客服", "Help / support"), CallbackData: "help"},
	}}}
}

func (a *App) orderCreationAvailable(ctx context.Context, uid int64) bool {
	s := a.sessions.Get(uid)
	if !a.sessionOK(ctx, uid, s.Err) {
		return false
	}
	if s.OrderGuard == nil {
		return true
	}
	a.send(ctx, uid, orderUnknownText(s.Locale), orderGuardKeyboard(s.Locale))
	return false
}

func (a *App) beginOrderCreation(uid int64, draftID string) (string, error) {
	id, claimed := rand.Text(), false
	s := a.sessions.Update(uid, func(s *session.State) {
		if s.OrderGuard == nil && s.Purchase != nil && s.Purchase.ID == draftID && s.Purchase.Submitting {
			s.OrderGuard = &session.OrderCreateGuard{ID: id, Status: "posting", LeaseUntil: time.Now().Add(3 * time.Minute)}
			claimed = true
		}
	})
	if s.Err != nil {
		return "", s.Err
	}
	if !claimed {
		return "", errOrderUnconfirmed
	}
	return id, nil
}

func (a *App) finishOrderCreation(uid int64, id string, result error) bool {
	changed := false
	s := a.sessions.Update(uid, func(s *session.State) {
		if g := s.OrderGuard; g != nil && g.ID == id {
			if orderCreationUnknown(result) {
				g.Status = "unknown"
				g.ReviewMessageID = 0
			} else {
				s.OrderGuard = nil
			}
			changed = true
		}
	})
	return s.Err == nil && changed
}

// Only a successfully owner-scoped order-history read offers manual recovery.
func (a *App) markOrderGuardReviewed(ctx context.Context, uid int64, reviewedID string) {
	s := a.sessions.Get(uid)
	if s.Err != nil || reviewedID == "" || s.OrderGuard == nil || s.OrderGuard.ID != reviewedID || s.OrderGuard.Status != "unknown" {
		return
	}
	id := s.OrderGuard.ID
	text := orderUnknownText(s.Locale) + "\n\n" + tr(s.Locale,
		"确认已核对订单和客服结果后，才可解除下单保护。解除后不会重发原请求，也不会取消任何订单。",
		"確認已核對訂單與客服結果後，才可解除下單保護。解除後不會重送原請求，也不會取消任何訂單。",
		"Only unlock after checking your orders and, if necessary, support. Unlocking does not resend the request or cancel any order.")
	m, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: uid, Text: text, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: tr(s.Locale, "已核对，解除下单保护", "已核對，解除下單保護", "Reviewed: unlock ordering"), CallbackData: "orderack:" + id},
	}}}})
	if err != nil {
		return
	}
	a.sessions.Update(uid, func(s *session.State) {
		if g := s.OrderGuard; g != nil && g.ID == id && g.Status == "unknown" {
			g.ReviewMessageID = m.ID
		}
	})
}

func (a *App) handleOrderGuardCallback(ctx context.Context, q *models.CallbackQuery) bool {
	const prefix = "orderack:"
	if len(q.Data) <= len(prefix) || q.Data[:len(prefix)] != prefix {
		return false
	}
	m := q.Message.Message
	if m == nil || m.Chat.ID != q.From.ID {
		return true
	}
	id, unlocked := q.Data[len(prefix):], false
	s := a.sessions.Update(q.From.ID, func(s *session.State) {
		if g := s.OrderGuard; g != nil && g.ID == id && g.Status == "unknown" && g.ReviewMessageID > 0 && g.ReviewMessageID == m.ID {
			s.OrderGuard = nil
			unlocked = true
		}
	})
	if !a.sessionOK(ctx, q.From.ID, s.Err) {
		return true
	}
	text := orderUnknownText(s.Locale)
	if unlocked {
		text = tr(s.Locale, "下单保护已解除，原请求不会重发。", "下單保護已解除，原請求不會重送。", "Ordering unlocked. The original request was not resent.")
	}
	_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	a.edit(ctx, q.From.ID, m.ID, text, a.homeKeyboard(s.Locale))
	return true
}
