package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/storage"

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
	sourceKey := storage.SourceKeyFromParsed(string(parsed.LinkType), parsed.VideoID)

	// 1. Проверяем кэш по source_key
	cached, err := b.store.Lookup(sourceKey)
	if err == nil {
		// Кэш-хит — отправляем по file_id мгновенно
		b.log.Info("cache hit",
			zap.String("source_key", sourceKey),
			zap.Int64("hit_count", cached.HitCount+1),
		)
		video := tgbotapi.NewVideo(chatID, tgbotapi.FileID(cached.TgFileID))
		video.Caption = "🎬 Видео"
		video.SupportsStreaming = true
		if err := b.sender.Send(video); err != nil {
			b.log.Error("failed to send cached video", zap.Error(err))
			b.sender.Text(chatID, "❌ Не удалось отправить видео.")
		}
		return
	}
	if !errors.Is(err, storage.ErrNotFound) {
		b.log.Error("cache lookup error", zap.Error(err))
	}

	// 2. Кэш-мисс — скачиваем
	b.sender.Text(chatID, "⏳ Скачиваю видео...")

	result, err := download.DownloadVideo(ctx, parsed.Raw, b.cfg.Proxy, b.log)
	if err != nil {
		b.log.Error("video download failed", zap.Error(err), zap.String("url", parsed.Raw))
		b.sender.Text(chatID, "❌ Не удалось скачать видео. Попробуйте позже.")
		return
	}
	defer cleanup(result.FilePath, b.log)

	// 3. Проверяем размер
	info, err := os.Stat(result.FilePath)
	if err != nil {
		b.log.Error("failed to stat downloaded file", zap.Error(err))
		b.sender.Text(chatID, "❌ Ошибка чтения файла.")
		return
	}

	fileSize := info.Size()

	if fileSize > b.cfg.MaxDownloadBytes {
		b.sender.Text(chatID, fmt.Sprintf(
			"❌ Видео слишком большое (%d МБ). Лимит: %d МБ.",
			fileSize/(1024*1024), b.cfg.MaxDownloadBytes/(1024*1024),
		))
		return
	}

	if fileSize > telegramMaxFileSize {
		b.sender.Text(chatID, fmt.Sprintf(
			"❌ Видео слишком большое для Telegram (%d МБ). Лимит: 50 МБ.",
			fileSize/(1024*1024),
		))
		return
	}

	// 4. Читаем файл и считаем SHA256
	fileData, err := os.ReadFile(result.FilePath)
	if err != nil {
		b.log.Error("failed to read downloaded file", zap.Error(err))
		b.sender.Text(chatID, "❌ Ошибка чтения файла.")
		return
	}

	hash := sha256.Sum256(fileData)
	hashHex := hex.EncodeToString(hash[:])

	// 5. Проверяем дедупликацию по SHA256 — может тот же файл уже был по другой ссылке
	if dedup, err := b.store.LookupBySHA256(hashHex); err == nil {
		b.log.Info("dedup hit by sha256",
			zap.String("sha256", hashHex),
			zap.String("existing_key", dedup.SourceKey),
		)
		video := tgbotapi.NewVideo(chatID, tgbotapi.FileID(dedup.TgFileID))
		video.Caption = "🎬 Видео"
		video.SupportsStreaming = true
		if err := b.sender.Send(video); err == nil {
			// Сохраняем новый source_key с тем же file_id
			_ = b.store.Upsert(&storage.MediaCache{
				SourceKey:      sourceKey,
				SHA256:         hashHex,
				TgFileID:       dedup.TgFileID,
				TgFileUniqueID: dedup.TgFileUniqueID,
				SizeBytes:      fileSize,
			})
			return
		}
		b.log.Warn("dedup send failed, uploading fresh", zap.Error(err))
	}

	// 6. Отправляем файл в Telegram
	fileBytes := tgbotapi.FileBytes{Name: parsed.VideoID + ".mp4", Bytes: fileData}
	video := tgbotapi.NewVideo(chatID, fileBytes)
	video.Caption = "🎬 Видео"
	video.SupportsStreaming = true

	resp, sendErr := b.sender.SendWithResponse(video)
	if sendErr != nil {
		b.log.Error("failed to send video to telegram", zap.Error(sendErr))
		b.sender.Text(chatID, "❌ Не удалось отправить видео в Telegram.")
		return
	}

	// 7. Извлекаем file_id из ответа Telegram и сохраняем в кэш
	if resp.Video != nil {
		entry := &storage.MediaCache{
			SourceKey:      sourceKey,
			SHA256:         hashHex,
			TgFileID:       resp.Video.FileID,
			TgFileUniqueID: resp.Video.FileUniqueID,
			SizeBytes:      fileSize,
		}
		if err := b.store.Upsert(entry); err != nil {
			b.log.Error("failed to save cache entry", zap.Error(err))
		} else {
			b.log.Info("cached video",
				zap.String("source_key", sourceKey),
				zap.String("file_id", resp.Video.FileID),
			)
		}
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
