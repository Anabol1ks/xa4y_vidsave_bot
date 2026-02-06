package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"xa4yy_vidsave/internal/bot"
	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/logger"
	"xa4yy_vidsave/internal/storage"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	_ = godotenv.Load()
	isDev := os.Getenv("ENV") == "development"
	if err := logger.Init(isDev); err != nil {
		panic(err)
	}
	defer logger.Sync()

	log := logger.L()
	cfg := config.Load(log)

	store, err := storage.New(cfg.DatabaseURL, log)
	if err != nil {
		log.Fatal("failed to connect to database", zap.Error(err))
	}
	defer store.Close()

	b, err := bot.New(cfg, log, store)
	if err != nil {
		log.Fatal("failed to create bot", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	b.Run(ctx)
}
