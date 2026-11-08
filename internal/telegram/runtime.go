package telegram

import (
	"context"
	"strings"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func (a *App) PublicTelegramConfig() dujiao.TelegramPublicConfig {
	return a.botConfig().PublicTelegram
}

// ConfigurationWarnings exposes fixed diagnostic codes, never credentials.
// Token changes are applied by an explicit restart, not during active requests.
func (a *App) ConfigurationWarnings(ctx context.Context) []string {
	cfg := a.botConfig()
	var warnings []string
	if cfg.BotToken != "" && cfg.BotToken != a.bot.Token() {
		warnings = append(warnings, "bot_token_changed_restart_required")
	}
	if cfg.PublicTelegram.Enabled {
		me, err := a.bot.GetMe(ctx)
		if err != nil {
			warnings = append(warnings, "telegram_identity_check_failed")
		} else if !strings.EqualFold(strings.TrimPrefix(cfg.PublicTelegram.BotUsername, "@"), me.Username) {
			warnings = append(warnings, "site_telegram_bot_mismatch")
		}
	}
	return warnings
}
