package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"

	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// --- Команды ---

func (b *Bot) handleCommand(chatID int64, msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		b.sender.Text(chatID,
			"👋 Привет! Отправь мне ссылку на видео из TikTok или Instagram, и я скачаю его без водяного знака.",
		)
	case "help":
		b.sender.Text(chatID,
			"📖 Поддерживаемые платформы:\n"+
				"• TikTok — ссылка вида tiktok.com/@user/video/123\n"+
				"• Instagram — ссылка вида instagram.com/reel/ABC\n\n"+
				"Просто отправь ссылку, и я пришлю видео.",
		)
	default:
		b.sender.Text(chatID, "Неизвестная команда. Попробуй /help")
	}
}

// --- Ошибки парсинга ---

func (b *Bot) handleParseError(chatID int64, text string, err error) {
	b.log.Info("link rejected", zap.Error(err), zap.String("text", text))

	switch err {
	case link.ErrNotURL:
		b.sender.Text(chatID, "Это не похоже на корректный URL.")
	case link.ErrNotAllowedHost:
		b.sender.Text(chatID, "❌ Домен не поддерживается. Поддерживаются: TikTok, Instagram.")
	case link.ErrUnknownFormat:
		b.sender.Text(chatID, "❌ Формат ссылки не распознан. Пришли прямую ссылку на видео.")
	default:
		b.sender.Text(chatID, "❌ Ошибка обработки ссылки: "+err.Error())
	}
}

// --- Скачивание и отправка видео ---

// Telegram Bot API лимит — 50 MB для отправки видео.
const telegramMaxFileSize = 50 * 1024 * 1024

func (b *Bot) handleDownload(ctx context.Context, chatID int64, parsed link.Parsed) {
	b.sender.Text(chatID, "⏳ Скачиваю видео...")

	result, err := download.DownloadVideo(ctx, parsed.Raw, b.cfg.Proxy, b.log)
	if err != nil {
		b.log.Error("video download failed", zap.Error(err), zap.String("url", parsed.Raw))
		b.sender.Text(chatID, "❌ Не удалось скачать видео. Попробуйте позже.")
		return
	}
	// Гарантируем очистку tmp в любом случае
	defer cleanup(result.FilePath, b.log)

	// Проверяем размер файла БЕЗ чтения в память
	info, err := os.Stat(result.FilePath)
	if err != nil {
		b.log.Error("failed to stat downloaded file", zap.Error(err))
		b.sender.Text(chatID, "❌ Ошибка чтения файла.")
		return
	}

	fileSize := info.Size()

	// Лимит из конфига (MaxDownloadBytes)
	if fileSize > b.cfg.MaxDownloadBytes {
		b.log.Warn("file exceeds config limit",
			zap.Int64("size", fileSize),
			zap.Int64("max", b.cfg.MaxDownloadBytes),
		)
		b.sender.Text(chatID, fmt.Sprintf(
			"❌ Видео слишком большое (%d МБ). Лимит: %d МБ.",
			fileSize/(1024*1024),
			b.cfg.MaxDownloadBytes/(1024*1024),
		))
		return
	}

	// Лимит Telegram Bot API (50 MB)
	if fileSize > telegramMaxFileSize {
		b.log.Warn("file exceeds Telegram limit",
			zap.Int64("size", fileSize),
		)
		b.sender.Text(chatID, fmt.Sprintf(
			"❌ Видео слишком большое для Telegram (%d МБ). Лимит: 50 МБ.",
			fileSize/(1024*1024),
		))
		return
	}

	// Читаем файл
	fileData, err := os.ReadFile(result.FilePath)
	if err != nil {
		b.log.Error("failed to read downloaded file", zap.Error(err))
		b.sender.Text(chatID, "❌ Ошибка чтения файла.")
		return
	}

	fileBytes := tgbotapi.FileBytes{Name: parsed.VideoID + ".mp4", Bytes: fileData}
	video := tgbotapi.NewVideo(chatID, fileBytes)
	video.Caption = "🎬 Видео"
	video.SupportsStreaming = true

	if err := b.sender.Send(video); err != nil {
		b.log.Error("failed to send video to telegram", zap.Error(err))
		b.sender.Text(chatID, "❌ Не удалось отправить видео в Telegram.")
		return
	}

	b.log.Info("video sent successfully",
		zap.String("video_id", parsed.VideoID),
		zap.Int64("size_bytes", fileSize),
	)
}

// cleanup удаляет скачанный файл и его родительскую tmp-директорию.
func cleanup(filePath string, log *zap.Logger) {
	if filePath == "" {
		return
	}
	dir := filepath.Dir(filePath)
	if err := os.RemoveAll(dir); err != nil {
		log.Warn("failed to cleanup tmp dir", zap.Error(err), zap.String("dir", dir))
	}
}
