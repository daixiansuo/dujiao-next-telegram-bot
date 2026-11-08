package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/config"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/dunea/dujiao-next-telegram-bot/internal/setup"
	telegramapp "github.com/dunea/dujiao-next-telegram-bot/internal/telegram"
)

const version = "1.0.0-dev"

func main() {
	if err := command(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Version checks must work before loading credentials or making network calls.
func command(args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "setup" {
		return setup.Run(args[1:], output, os.Getenv)
	}
	if len(args) == 0 {
		return run()
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		_, err := fmt.Fprintln(output, version)
		return err
	}
	return errors.New("usage: dujiao-next-telegram-bot [--version | setup init | setup check]")
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := newLogger(cfg.LogLevel)
	var auditWriter io.Writer = os.Stdout
	if cfg.AuditFile != "" {
		file, err := operations.OpenAuditFile(cfg.AuditFile, 10<<20, 5)
		if err != nil {
			return err
		}
		defer file.Close()
		auditWriter = io.MultiWriter(os.Stdout, file)
	}

	api, err := dujiao.NewClient(cfg.DujiaoBaseURL, cfg.DujiaoChannelKey, cfg.DujiaoChannelSecret, cfg.PollTimeout)
	if err != nil {
		return err
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	botConfig, err := api.BotConfig(startupCtx)
	startupCancel()
	if err != nil {
		return fmt.Errorf("load bot config from Dujiao-Next: %w", err)
	}
	token := cfg.TelegramBotToken
	if token == "" {
		token = botConfig.Config.BotToken
	}
	defaultLocale := botConfig.Config.DefaultLocale
	if defaultLocale == "" {
		defaultLocale = cfg.DefaultLocale
	}
	var sessions session.Store = session.NewMemory(defaultLocale)
	if cfg.RedisURL != "" {
		sessions, err = session.NewRedis(cfg.RedisURL, cfg.SessionPrefix(), defaultLocale, cfg.SessionEncryptionKey)
		if err != nil {
			return fmt.Errorf("initialize Redis sessions: %w", err)
		}
		logger.Info("Redis sessions enabled")
	} else {
		logger.Warn("memory sessions enabled; restart protection requires Redis")
	}
	defer sessions.Close()
	app, err := telegramapp.New(token, api, botConfig.Config, defaultLocale, cfg.PollTimeout, cfg.TelegramAPIURL, logger, sessions)
	if err != nil {
		return err
	}
	// Audit INFO records remain enabled even when application logs use WARN/ERROR.
	app.ConfigureOperations(cfg.AdminUserIDs, cfg.RatePolicy, slog.New(slog.NewJSONHandler(auditWriter, nil)))
	app.ConfigureBroadcast(cfg.BroadcastInterval, cfg.BroadcastMaxRecipients)
	if cfg.SupportEnabled {
		if err := app.ConfigureSupport(cfg.SupportChatID, cfg.SupportUserIDs); err != nil {
			return fmt.Errorf("configure customer support: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := httpServer(cfg.HTTPListen, cfg.DujiaoChannelKey, cfg.DujiaoChannelSecret, app, logger)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("health server listening", "address", cfg.HTTPListen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	go heartbeatLoop(ctx, logger, api, app)
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		logger.Info("Telegram bot started", "version", version)
		app.Start(ctx)
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		stop()
		runErr = fmt.Errorf("health server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown health server", "error", err)
	}
	select {
	case <-appDone:
	case <-shutdownCtx.Done():
		logger.Warn("bot shutdown deadline reached")
	}
	logger.Info("Telegram bot stopped")
	return runErr
}

const configSyncInterval = time.Minute

func heartbeatLoop(ctx context.Context, logger *slog.Logger, api *dujiao.Client, app *telegramapp.App) {
	syncAndHeartbeat(ctx, logger, api, app)
	ticker := time.NewTicker(configSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncAndHeartbeat(ctx, logger, api, app)
		}
	}
}

// Configuration and heartbeat have independent deadlines. A slow/failed refresh
// must not consume the heartbeat's budget or falsely acknowledge a new version.
func syncAndHeartbeat(ctx context.Context, logger *slog.Logger, api *dujiao.Client, app *telegramapp.App) {
	var warnings []string
	var applied []int
	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	remote, err := api.BotConfig(configCtx)
	cancel()
	if err != nil {
		warnings = append(warnings, "configuration_refresh_failed")
		logger.Warn("refresh bot configuration unavailable")
	} else {
		publicCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		public, publicErr := api.PublicTelegramConfig(publicCtx)
		stop()
		if publicErr != nil {
			public = app.PublicTelegramConfig()
			warnings = append(warnings, "public_configuration_refresh_failed")
		}
		remote.Config.PublicTelegram = public
		if remote.Config.ConfigVersion == 0 {
			remote.Config.ConfigVersion = remote.ConfigVersion
		}
		app.UpdateConfig(remote.Config)
		checkCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		configWarnings := app.ConfigurationWarnings(checkCtx)
		stop()
		warnings = append(warnings, configWarnings...)
		if len(configWarnings) == 0 {
			applied = []int{remote.Config.ConfigVersion}
		}
	}
	heartbeatCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if err := api.Heartbeat(heartbeatCtx, version, "long_polling", warnings, applied...); err != nil {
		logger.Warn("send channel heartbeat unavailable")
	}
}

func httpServer(address, channelKey, channelSecret string, notifier callback.Notifier, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if health, ok := notifier.(interface{ SessionHealth() error }); ok && health.SessionHealth() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("{\"status\":\"session_unavailable\"}\n"))
			return
		}
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	var callbackOptions []callback.Options
	if provider, ok := notifier.(interface{ CallbackOptions() callback.Options }); ok {
		callbackOptions = append(callbackOptions, provider.CallbackOptions())
	}
	callback.New(channelKey, channelSecret, notifier, logger, callbackOptions...).Register(mux)
	return &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
