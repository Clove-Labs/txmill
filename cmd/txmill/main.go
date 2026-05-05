package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clove-labs/txmill/internal/alert"
	"github.com/clove-labs/txmill/internal/api"
	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/chain"
	"github.com/clove-labs/txmill/internal/config"
	"github.com/clove-labs/txmill/internal/gas"
	"github.com/clove-labs/txmill/internal/keystore"
	"github.com/clove-labs/txmill/internal/relay"
	"github.com/clove-labs/txmill/internal/signer"
	"github.com/clove-labs/txmill/internal/store"
	"github.com/clove-labs/txmill/internal/treasury"
	"github.com/clove-labs/txmill/internal/webhook"
)

func buildNotifier(cfg *config.Config, logger *slog.Logger) alert.Notifier {
	transports := alert.Multi{}
	if cfg.AlertWebhookURL != "" {
		transports = append(transports, alert.NewWebhook(cfg.AlertWebhookURL))
	}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		transports = append(transports, alert.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID))
	}
	if len(transports) == 0 {
		logger.Info("no alert transports configured; alerts will be discarded")
		return alert.Discard{}
	}
	logger.Info("alert transports configured", "count", len(transports),
		"throttle", time.Duration(cfg.AlertThrottleMs)*time.Millisecond)
	return alert.NewThrottled(transports, time.Duration(cfg.AlertThrottleMs)*time.Millisecond)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	if cfg.DBURL == "" {
		logger.Error("TXMILL_DB_URL is required")
		os.Exit(2)
	}
	if cfg.RPCURL == "" {
		logger.Error("TXMILL_RPC_URL is required")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, cfg.DBURL)
	if err != nil {
		logger.Error("db pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	ks, err := keystore.New(cfg.KeystoreDir, cfg.KeystorePassword)
	if err != nil {
		logger.Error("keystore", "err", err)
		os.Exit(1)
	}

	chainClient, err := chain.Dial(ctx, cfg.RPCURL, cfg.ChainID)
	if err != nil {
		logger.Error("chain dial", "err", err)
		os.Exit(1)
	}
	defer chainClient.Close()

	if report, err := relay.Recover(ctx, pool); err != nil {
		logger.Error("startup recovery scan failed", "err", err)
	} else {
		logger.Info("startup recovery",
			"inflight_attempts", report.InflightAttempts,
			"pending_webhook_deliveries", report.PendingWebhookDeliveries,
			"stuck_requests", len(report.StuckRequests),
		)
		for _, s := range report.StuckRequests {
			logger.Warn("stuck pending request (no tx_attempt — likely crashed before submit)",
				"request_id", s.RequestID,
				"app_id", s.AppID,
				"created_at", s.CreatedAt,
			)
		}
	}

	appSvc := app.NewService(pool, ks)
	signerPool := signer.NewPool(ks, chainClient, appSvc)
	relaySvc := relay.NewService(pool, chainClient, signerPool)

	webhookSvc := webhook.NewService(pool, relaySvc, logger)
	relaySvc.SetWebhooks(webhookSvc)

	watcher := relay.NewWatcher(
		pool,
		chainClient,
		time.Duration(cfg.WatcherIntervalMs)*time.Millisecond,
		int(cfg.WatcherBatchSize),
		logger,
	)
	watcher.SetWebhooks(webhookSvc)
	webhookWorker := webhook.NewWorker(
		pool,
		time.Duration(cfg.WatcherIntervalMs)*time.Millisecond,
		int(cfg.WatcherBatchSize),
		logger,
	)
	bgCtx, cancelBg := context.WithCancel(context.Background())
	defer cancelBg()
	notifier := buildNotifier(cfg, logger)
	gasWorker := gas.NewWorker(
		pool,
		chainClient,
		ks,
		notifier,
		time.Duration(cfg.GasIntervalMs)*time.Millisecond,
		logger,
	)
	go watcher.Run(bgCtx)
	go webhookWorker.Run(bgCtx)
	go gasWorker.Run(bgCtx)

	treasurySvc := treasury.NewService(pool, chainClient, ks)

	handlers := &api.Handlers{
		Apps:     appSvc,
		Relay:    relaySvc,
		Chain:    chainClient,
		Treasury: treasurySvc,
	}
	e := api.NewRouter(logger, handlers)

	go func() {
		logger.Info("http server starting", "addr", cfg.APIListen, "chain_id", cfg.ChainID)
		if err := e.Start(cfg.APIListen); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	cancelBg()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.Shutdown(shutdownCtx)
}
