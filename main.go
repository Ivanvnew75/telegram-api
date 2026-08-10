package main

import (
	"errors"
	"log"
	"log/slog"
	"net/http"
	"sync"

	"github.com/Ivanvnew75/libs/common"

	"github.com/Ivanvnew75/telegram-api/api"
	"github.com/Ivanvnew75/telegram-api/bot"
	"github.com/Ivanvnew75/telegram-api/config"
	"github.com/Ivanvnew75/telegram-api/telegram"
	"github.com/Ivanvnew75/telegram-api/usersclient"
)

// Значения подставляются линкером при сборке (-ldflags -X).
// Версия в логах — это то, что первым спрашивают при разборе инцидента:
// «а на какой версии это было?».
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cfg, err := config.Load(version)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	logger := common.NewLogger(cfg.Service, cfg.Version, cfg.LogFormat, cfg.LogLevel)
	logger.Info("starting",
		slog.String("commit", commit),
		slog.Bool("polling_enabled", cfg.PollingEnabled))

	tg := telegram.New(cfg.TelegramAPIURL, cfg.TelegramToken, cfg.PollTimeout)
	users := usersclient.New(cfg.UsersServiceURL, cfg.HTTPTimeout, cfg.HTTPRetries)

	// Контекст, который отменится по SIGTERM.
	ctx, stop := common.SignalContext()
	defer stop()

	var wg sync.WaitGroup

	// Тип процесса №2 — опросчик. Включается переменной окружения.
	// Тот же образ, то же приложение; разное поведение задаётся конфигом
	// (Фактор 3), а не отдельной сборкой.
	if cfg.PollingEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bot.New(tg, users, logger, cfg.Question).Run(ctx, cfg.PollTimeout)
		}()
	}

	// HTTP-сервер поднимается ВСЕГДА, даже у опросчика.
	// Причина простая: без него у пода не будет ни liveness-, ни
	// readiness-пробы, и Kubernetes не сможет судить о его состоянии.
	srv := api.New(tg, users, logger).Echo()
	wg.Add(1)
	go func() {
		defer wg.Done()
		addr := ":" + cfg.Port
		logger.Info("http server listening", slog.String("addr", addr))
		if err := srv.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", slog.String("error", err.Error()))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := common.ShutdownContext(cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.String("error", err.Error()))
	}

	// Ждём, пока опросчик доработает текущую итерацию.
	wg.Wait()
	logger.Info("stopped")
}
