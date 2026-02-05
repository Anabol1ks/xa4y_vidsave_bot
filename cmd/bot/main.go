package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/logger"

	"github.com/joho/godotenv"
	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		log.Fatal("failed to create bot", zap.Error(err))
	}
	log.Info("bot authorized", zap.String("username", bot.Self.UserName))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	log.Info("bot started, waiting for updates...")
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down gracefully")
			return
		case upd := <-updates:
			handleUpdate(ctx, log, bot, upd, cfg)
		}
	}
}

func handleUpdate(ctx context.Context, log *zap.Logger, bot *tgbotapi.BotAPI, upd tgbotapi.Update, cfg *config.Config) {
	if upd.Message == nil {
		return
	}

	msg := upd.Message
	chatID := msg.Chat.ID

	// Команды
	if msg.IsCommand() {
		switch msg.Command() {
		case "start":
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Привет! Пришли ссылку на видео с твоего хостинга."))
		case "help":
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Отправь URL. Сейчас я просто проверю и отвечу, дальше добавим обработку."))
		default:
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Неизвестная команда. /help"))
		}
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Пришли ссылку текстом."))
		return
	}

	parsed, err := link.Parse(text, cfg.AllowedHosts)
	if err != nil {
		log.Info("link rejected", zap.Error(err), zap.String("text", text))

		switch err {
		case link.ErrNotURL:
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Это не похоже на корректный URL."))
		case link.ErrNotAllowedHost:
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Домен/порт не разрешён (allowlist)."))
		case link.ErrUnknownFormat:
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Ссылка с разрешённого домена, но формат пути не поддержан."))
		default:
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Ошибка обработки ссылки: "+err.Error()))
		}
		return
	}

	log.Info("link accepted",
		zap.String("type", string(parsed.LinkType)),
		zap.String("video_id", parsed.VideoID),
		zap.String("host", parsed.Host),
		zap.String("path", parsed.Path),
	)

	switch parsed.LinkType {
	case link.TypeInstagram, link.TypeTikTok:
		handleDownload(ctx, log, bot, chatID, parsed, cfg)
	default:
		_, _ = bot.Send(tgbotapi.NewMessage(chatID,
			"✅ Ссылка принята\nТип: "+string(parsed.LinkType)+"\nID: "+parsed.VideoID+
				"\n\n⏳ Скачивание этого типа пока не реализовано.",
		))
	}
}

func handleDownload(ctx context.Context, log *zap.Logger, bot *tgbotapi.BotAPI, chatID int64, parsed link.Parsed, cfg *config.Config) {
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "⏳ Скачиваю видео..."))

	result, err := download.DownloadVideo(ctx, parsed.Raw, cfg.Proxy, log)
	if err != nil {
		log.Error("video download failed", zap.Error(err), zap.String("url", parsed.Raw))
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "❌ Не удалось скачать видео. Попробуйте позже."))
		return
	}
	defer os.Remove(result.FilePath)
	// Удаляем родительскую tmp-директорию
	defer os.RemoveAll(filepath.Dir(result.FilePath))

	fileData, err := os.ReadFile(result.FilePath)
	if err != nil {
		log.Error("failed to read downloaded file", zap.Error(err))
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "❌ Ошибка чтения файла."))
		return
	}

	if int64(len(fileData)) > cfg.MaxDownloadBytes {
		log.Warn("file too large", zap.Int("size", len(fileData)), zap.Int64("max", cfg.MaxDownloadBytes))
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "❌ Видео слишком большое для отправки."))
		return
	}

	fileBytes := tgbotapi.FileBytes{Name: parsed.VideoID + ".mp4", Bytes: fileData}
	video := tgbotapi.NewVideo(chatID, fileBytes)
	video.Caption = "🎬 Видео"
	video.SupportsStreaming = true

	if _, err := bot.Send(video); err != nil {
		log.Error("failed to send video to telegram", zap.Error(err))
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "❌ Не удалось отправить видео в Telegram."))
		return
	}

	log.Info("video sent successfully", zap.String("video_id", parsed.VideoID))
}
