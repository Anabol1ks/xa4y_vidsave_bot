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
			"скинь ссылку на TikTok, Instagram Reels или YouTube Shorts —\n" +
			"верну без водяного знака 🔥\n\n" +
			"канал → @XA4yy"
		reply := tgbotapi.NewMessage(chatID, text)
		reply.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL("📢 Канал", channelLink),
			),
		)
		b.sender.Send(reply)
	case "stats", "stats_text":
		if !msg.Chat.IsPrivate() {
			return
		}
		if !statsAuthorized(msg, b.cfg.AdminTelegramIDs) {
			b.sender.Text(chatID, "🔒 статистика доступна только владельцу")
			return
		}
		if msg.Command() == "stats_text" || !b.cfg.AnalyticsWidgetsEnabled {
			b.sendTextStats(chatID)
			return
		}
		if err := b.sendAnalyticsWidget(chatID); err != nil {
			b.log.Warn("sendWidget failed; falling back to text stats", zap.Error(err))
			b.sendTextStats(chatID)
		}
	case "id":
		if msg.Chat.IsPrivate() && msg.From != nil {
			b.sender.Text(chatID, fmt.Sprintf("🪪 Твой Telegram ID: %d", msg.From.ID))
		}
	case "help":
		b.sender.Text(chatID,
			"📌 что умею:\n\n"+
				"• TikTok — ссылка на видео\n"+
				"• Instagram — ссылка на reel\n"+
				"• YouTube — ссылка на Shorts\n"+
				"• в группах — отвечаю видео на первую ссылку в сообщении\n\n"+
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
		b.sender.Text(chatID, "такой домен не поддерживаю 😕\n\nпока умею TikTok, Instagram и YouTube Shorts"+errorContact)
	case link.ErrUnknownFormat:
		b.sender.Text(chatID, "не могу разобрать ссылку 🤔\nкинь прямую ссылку на видео"+errorContact)
	default:
		b.sender.Text(chatID, "что-то пошло не так: "+err.Error()+errorContact)
	}
}

// --- Скачивание и отправка видео ---

// Telegram Bot API лимит — 50 MB для отправки видео.
const telegramMaxFileSize = 50 * 1024 * 1024

func (b *Bot) handleDownload(ctx context.Context, userID int64, chatKind string, chatID int64, replyToMessageID int, parsed link.Parsed) {
	started := time.Now()
	status := "internal_error"
	cacheKind := "none"
	defer func() {
		if userID <= 0 {
			return
		}
		event := storage.MediaRequest{
			TelegramUserID: userID,
			Platform:       string(parsed.LinkType),
			ChatKind:       chatKind,
			Status:         status,
			CacheKind:      cacheKind,
			DurationMS:     time.Since(started).Milliseconds(),
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := b.store.RecordRequest(saveCtx, event); err != nil {
			b.log.Error("failed to record analytics request", zap.Error(err))
		}
	}()
	sourceKey := storage.SourceKeyFromParsed(string(parsed.LinkType), parsed.VideoID)
	staged:=b.downloadWidgetActive(userID)
	if staged {
		release, err := b.downloadGate.begin(userID, chatID, sourceKey, chatKind == "group")
		if err != nil {
			status = "busy"
			if b.downloadGate.shouldNotify(chatID) {
				b.sender.TextReply(chatID, replyToMessageID, "⏳ Слишком много запросов. Дождись завершения текущего видео или отправь ссылку позже.")
			}
			return
		}
		defer release()
	}
	replyText := func(text string) {
		b.sender.TextReply(chatID, replyToMessageID, text)
	}

	// 1. Проверяем кэш по source_key
	cached, err := b.store.Lookup(sourceKey)
	if err == nil {
		cacheKind = "source"
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
		setReply(&video.BaseChat, replyToMessageID)
		if err := b.sender.Send(video); err != nil {
			status = "send_error"
			b.log.Error("failed to send cached video", zap.Error(err))
			replyText("не удалось отправить видео 😢")
		} else {
			status = "success"
		}
		return
	}
	if !errors.Is(err, storage.ErrNotFound) {
		b.log.Error("cache lookup error", zap.Error(err))
	}

	if staged {
		releaseQueue,err:=b.reserveDownload()
		if err!=nil {
			status="busy"
			if b.downloadGate.shouldNotify(chatID) {
				replyText("⏳ Все слоты заняты. Попробуй отправить другую ссылку позже.")
			}
			return
		}
		defer releaseQueue()
	}
	// 2. Кэш-мисс — скачиваем. Status message is optional and never
	// blocks a download when Telegram widgets are unsupported.
	progress := b.beginDownloadStatus(ctx, chatKind, chatID, replyToMessageID, parsed.LinkType)
	defer func() { progress.finish(status == "success") }()
	progress.stage("downloading")
	var callback func(download.ProgressEvent)
	if progress.widget != nil {
		callback = progress.progress
	}
	var result *download.VideoResult
	if staged {
		result, err = b.downloadVideoWithProgress(ctx, parsed, callback)
	} else {
		result, err = b.downloadVideoWithLimit(ctx, parsed)
	}
	if err != nil {
		b.log.Error("video download failed", zap.Error(err), zap.String("url", parsed.Raw))

		switch {
		case errors.Is(err, ErrDownloadBusy):
			status = "busy"
			if b.downloadGate.shouldNotify(chatID) {
				replyText("⏳ Сейчас все загрузки заняты. Отправь ссылку чуть позже.")
			}
		case errors.Is(err, download.ErrYtDlpAuth):
			status = "auth_required"
			replyText("эта ссылка требует вход в аккаунт и в публичном режиме не скачивается 😕\nпопробуй другую публичную ссылку" + errorContact)
		case errors.Is(err, download.ErrVideoTooLong):
			status = "too_long"
			replyText("YouTube Shorts должен быть не длиннее 3 минут 😕")
		case errors.Is(err, download.ErrYtDlpUnsupported):
			status = "unsupported"
			replyText("эта ссылка ведёт не на видео или yt-dlp не умеет её скачивать 😕\nпопробуй другую ссылку" + errorContact)
		default:
			status = "download_error"
			replyText("не удалось скачать видео 😕\nпопробуй позже" + errorContact)
		}
		return
	}
	defer cleanup(result.FilePath, b.log)
	progress.stage("preparing")

	// 3. Проверяем размер
	info, err := os.Stat(result.FilePath)
	if err != nil {
		b.log.Error("failed to stat downloaded file", zap.Error(err))
		replyText("ошибка чтения файла 😕" + errorContact)
		return
	}

	fileSize := info.Size()

	if fileSize > b.cfg.MaxDownloadBytes {
		status = "too_large"
		replyText(fmt.Sprintf(
			"видео слишком большое (%d МБ), лимит %d МБ 😬",
			fileSize/(1024*1024), b.cfg.MaxDownloadBytes/(1024*1024),
		))
		return
	}

	if fileSize > telegramMaxFileSize {
		status = "too_large"
		replyText(fmt.Sprintf(
			"видео слишком большое для Telegram (%d МБ), лимит 50 МБ 😬",
			fileSize/(1024*1024),
		))
		return
	}

	// 4. Читаем файл и считаем SHA256
	fileData, err := os.ReadFile(result.FilePath)
	if err != nil {
		b.log.Error("failed to read downloaded file", zap.Error(err))
		replyText("ошибка чтения файла 😕" + errorContact)
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
		setReply(&video.BaseChat, replyToMessageID)
		progress.stage("sending")
		if err := b.sender.Send(video); err == nil {
			status = "success"
			cacheKind = "sha256"
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
	setReply(&video.BaseChat, replyToMessageID)

	progress.stage("sending")
	resp, sendErr := b.sender.SendWithResponse(video)
	if sendErr != nil {
		status = "send_error"
		b.log.Error("failed to send video to telegram", zap.Error(sendErr))
		replyText("не удалось отправить видео 😢" + errorContact)
		return
	}
	if resp == nil || resp.Video == nil {
		status = "send_error"
		b.log.Error("telegram returned an empty video response")
		replyText("не удалось отправить видео 😢" + errorContact)
		return
	}

	status = "success"
	// 7. Извлекаем file_id из ответа Telegram и сохраняем в кэш
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

	b.log.Info("video sent successfully",
		zap.String("video_id", parsed.VideoID),
		zap.Int64("size_bytes", fileSize),
	)
}

// downloadVideoWithProgress preserves the legacy exact yt-dlp args when the
// feature is off. The feature path bounds both queue size and execution time.
func (b *Bot) downloadVideoWithProgress(ctx context.Context, parsed link.Parsed, callback func(download.ProgressEvent)) (*download.VideoResult, error) {
	if !b.cfg.DownloadWidgetsEnabled {
		return b.downloadVideoWithLimit(ctx, parsed)
	}
	var result *download.VideoResult
	err := b.stagedDownload(ctx, func() error {
		jobCtx, cancel := context.WithTimeout(ctx, b.cfg.DownloadExecutionTimeout)
		defer cancel()
		var err error
		if callback != nil {
			result, err = download.DownloadVideoWithProgress(jobCtx, parsed.Raw, b.cfg.Proxy, parsed.LinkType, b.log, callback)
		} else {
			result, err = download.DownloadVideo(jobCtx, parsed.Raw, b.cfg.Proxy, parsed.LinkType, b.log)
		}
		return err
	})
	return result, err
}

func (b *Bot) downloadVideoWithLimit(ctx context.Context, parsed link.Parsed) (*download.VideoResult, error) {
	select {
	case b.downloadSlots <- struct{}{}:
		defer func() { <-b.downloadSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return download.DownloadVideo(ctx, parsed.Raw, b.cfg.Proxy, parsed.LinkType, b.log)
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
	if q.From != nil {
		b.trackUser(context.Background(), q.From)
	}
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
