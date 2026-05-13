package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/storage"

	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	channelLink  = "https://t.me/XA4yy"
	videoCaption = "🎬 @XA4yy"
	errorContact = "\n\nесли повторяется — напиши @gr1sha_44"
)

// --- Команды ---

func (b *Bot) handleCommand(chatID int64, msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		text := "Барев! 👋\n\n" +
			"скинь ссылку на видео из TikTok или Instagram —\n" +
			"верну без водяного знака 🔥\n\n" +
			"канал → @XA4yy"
		reply := tgbotapi.NewMessage(chatID, text)
		reply.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL("📢 Канал", channelLink),
			),
		)
		b.sender.Send(reply)
	case "help":
		b.sender.Text(chatID,
			"📌 что умею:\n\n"+
				"• TikTok — ссылка на видео\n"+
				"• Instagram — ссылка на reel\n\n"+
				"просто кидай ссылку 👇",
		)
	default:
		b.sender.Text(chatID, "хз такую команду 🤷‍♂️ жми /help")
	}
}

// --- Ошибки парсинга ---

func (b *Bot) handleParseError(chatID int64, text string, err error) {
	b.log.Info("link rejected", zap.Error(err), zap.String("text", text))

	switch err {
	case link.ErrNotURL:
		b.sender.Text(chatID, "это не похоже на ссылку 🧐")
	case link.ErrNotAllowedHost:
		b.sender.Text(chatID, "такой домен не поддерживаю 😕\n\nпока умею только TikTok и Instagram"+errorContact)
	case link.ErrUnknownFormat:
		b.sender.Text(chatID, "не могу разобрать ссылку 🤔\nкинь прямую ссылку на видео"+errorContact)
	default:
		b.sender.Text(chatID, "что-то пошло не так: "+err.Error()+errorContact)
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
		kb := shareKeyboard(sourceKey)
		video := tgbotapi.NewVideo(chatID, tgbotapi.FileID(cached.TgFileID))
		video.Caption = videoCaption
		video.SupportsStreaming = true
		video.ReplyMarkup = kb
		if err := b.sender.Send(video); err != nil {
			b.log.Error("failed to send cached video", zap.Error(err))
			b.sender.Text(chatID, "не удалось отправить видео 😢")
		}
		return
	}
	if !errors.Is(err, storage.ErrNotFound) {
		b.log.Error("cache lookup error", zap.Error(err))
	}

	// 2. Кэш-мисс — скачиваем
	statusMsg := b.sender.TextWithResponse(chatID, "⏳ сек, качаю")

	// Анимация загрузки в фоне
	stopAnim := make(chan struct{})
	if statusMsg != nil {
		go func() {
			frames := []string{"⏳ сек, качаю.", "⏳ сек, качаю..", "⏳ сек, качаю...", "⏳ сек, качаю"}
			i := 0
			ticker := time.NewTicker(800 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopAnim:
					return
				case <-ticker.C:
					b.sender.EditText(chatID, statusMsg.MessageID, frames[i%len(frames)])
					i++
				}
			}
		}()
	}

	// Удаляем статус-сообщение при выходе
	defer func() {
		close(stopAnim)
		if statusMsg != nil {
			b.sender.Delete(chatID, statusMsg.MessageID)
		}
	}()

	result, err := download.DownloadVideo(ctx, parsed.Raw, b.cfg.Proxy, b.log)
	if err != nil {
		b.log.Error("video download failed", zap.Error(err), zap.String("url", parsed.Raw))

		switch {
		case errors.Is(err, download.ErrYtDlpAuth):
			b.sender.Text(chatID, "эта ссылка требует вход в аккаунт и в публичном режиме не скачивается 😕\nпопробуй другую публичную ссылку"+errorContact)
		case errors.Is(err, download.ErrYtDlpUnsupported):
			b.sender.Text(chatID, "эта ссылка ведёт не на видео или yt-dlp не умеет её скачивать 😕\nпопробуй другую ссылку"+errorContact)
		default:
			b.sender.Text(chatID, "не удалось скачать видео 😕\nпопробуй позже"+errorContact)
		}
		return
	}
	defer cleanup(result.FilePath, b.log)

	// 3. Проверяем размер
	info, err := os.Stat(result.FilePath)
	if err != nil {
		b.log.Error("failed to stat downloaded file", zap.Error(err))
		b.sender.Text(chatID, "ошибка чтения файла 😕"+errorContact)
		return
	}

	fileSize := info.Size()

	if fileSize > b.cfg.MaxDownloadBytes {
		b.sender.Text(chatID, fmt.Sprintf(
			"видео слишком большое (%d МБ), лимит %d МБ 😬",
			fileSize/(1024*1024), b.cfg.MaxDownloadBytes/(1024*1024),
		))
		return
	}

	if fileSize > telegramMaxFileSize {
		b.sender.Text(chatID, fmt.Sprintf(
			"видео слишком большое для Telegram (%d МБ), лимит 50 МБ 😬",
			fileSize/(1024*1024),
		))
		return
	}

	// 4. Читаем файл и считаем SHA256
	fileData, err := os.ReadFile(result.FilePath)
	if err != nil {
		b.log.Error("failed to read downloaded file", zap.Error(err))
		b.sender.Text(chatID, "ошибка чтения файла 😕"+errorContact)
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
		kb := shareKeyboard(sourceKey)
		video := tgbotapi.NewVideo(chatID, tgbotapi.FileID(dedup.TgFileID))
		video.Caption = videoCaption
		video.SupportsStreaming = true
		video.ReplyMarkup = kb
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
	kb := shareKeyboard(sourceKey)
	fileBytes := tgbotapi.FileBytes{Name: parsed.VideoID + ".mp4", Bytes: fileData}
	video := tgbotapi.NewVideo(chatID, fileBytes)
	video.Caption = videoCaption
	video.SupportsStreaming = true
	video.ReplyMarkup = kb

	resp, sendErr := b.sender.SendWithResponse(video)
	if sendErr != nil {
		b.log.Error("failed to send video to telegram", zap.Error(sendErr))
		b.sender.Text(chatID, "не удалось отправить видео 😢"+errorContact)
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

// --- Inline ---

// shareKeyboard возвращает клавиатуру с кнопкой «Поделиться».
func shareKeyboard(sourceKey string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.InlineKeyboardButton{
				Text:              "📤 Поделиться",
				SwitchInlineQuery: &sourceKey,
			},
		),
	)
}

// handleInlineQuery обрабатывает inline-запросы для кнопки «Поделиться».
func (b *Bot) handleInlineQuery(q *tgbotapi.InlineQuery) {
	text := strings.TrimSpace(q.Query)
	if text == "" {
		return
	}

	cached, err := b.store.Lookup(text)
	if err != nil {
		b.log.Debug("inline query: cache miss", zap.String("query", text))
		empty := tgbotapi.InlineConfig{InlineQueryID: q.ID, Results: []interface{}{}}
		b.api.Request(empty)
		return
	}

	kb := shareKeyboard(text)
	result := tgbotapi.NewInlineQueryResultCachedVideo(text, cached.TgFileID, "Видео без водяного знака")
	result.Caption = videoCaption
	result.ReplyMarkup = &kb

	resp := tgbotapi.InlineConfig{
		InlineQueryID: q.ID,
		Results:       []interface{}{result},
		CacheTime:     300,
	}

	if _, err := b.api.Request(resp); err != nil {
		b.log.Error("inline query failed", zap.Error(err))
	}
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
