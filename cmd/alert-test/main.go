package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/clove-labs/txmill/internal/alert"
	"github.com/clove-labs/txmill/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	transports := alert.Multi{}
	if cfg.AlertWebhookURL != "" {
		transports = append(transports, alert.NewWebhook(cfg.AlertWebhookURL))
	}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		transports = append(transports, alert.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID))
	}
	if len(transports) == 0 {
		fmt.Fprintln(os.Stderr, "no alert transports configured (set TXMILL_ALERT_WEBHOOK_URL or TXMILL_TELEGRAM_BOT_TOKEN+TXMILL_TELEGRAM_CHAT_ID)")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := transports.Notify(ctx, alert.Alert{
		Key:     "alert_test",
		Level:   alert.LevelInfo,
		Title:   "txmill alert test",
		Message: fmt.Sprintf("Test alert dispatched at %s", time.Now().Format(time.RFC3339)),
		Tags:    map[string]string{"source": "alert-test"},
	}); err != nil {
		logger.Error("alert test failed", "err", err)
		os.Exit(1)
	}
	logger.Info("alert test sent", "transports", len(transports))
}
