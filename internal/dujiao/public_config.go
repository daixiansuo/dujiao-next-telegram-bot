package dujiao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// TelegramPublicConfig contains only the public website login and Mini App settings.
// It does not contain Bot Tokens or OIDC secrets.
type TelegramPublicConfig struct {
	Enabled     bool   `json:"enabled"`
	BotUsername string `json:"bot_username"`
	MiniAppURL  string `json:"mini_app_url"`
}

// PublicTelegramConfig reads the existing storefront configuration. Public requests
// deliberately carry no Channel credentials, including when a redirect is returned.
func (c *Client) PublicTelegramConfig(ctx context.Context) (TelegramPublicConfig, error) {
	var result TelegramPublicConfig
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/public/config", nil)
	if err != nil {
		return result, fmt.Errorf("create public configuration request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return result, fmt.Errorf("request public configuration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, &APIError{HTTPStatus: response.StatusCode, StatusCode: response.StatusCode, Message: http.StatusText(response.StatusCode)}
	}
	var envelope struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Telegram TelegramPublicConfig `json:"telegram_auth"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope); err != nil {
		return result, fmt.Errorf("decode public configuration: %w", err)
	}
	if envelope.StatusCode != 0 {
		return result, &APIError{HTTPStatus: response.StatusCode, StatusCode: envelope.StatusCode, Message: "public configuration unavailable"}
	}
	return envelope.Data.Telegram, nil
}
