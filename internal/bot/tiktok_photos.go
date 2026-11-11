package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

const photoCaption = "📷 @XA4yy"

func (b *Bot) photoActive(userID int64) bool {
	if b == nil || b.cfg == nil || !b.cfg.TikTokPhotosEnabled {
		return false
	}
	if len(b.cfg.TikTokPhotoPilotIDs) == 0 {
		return true
	}
	_, ok := b.cfg.TikTokPhotoPilotIDs[userID]
	return ok
}

// handleTikTokPhotos reuses the existing bounded queue/cooldowns and ephemeral
// status message. Telegram's media albums have a hard limit of 2-10 items:
// use sendPhoto when the post contains just one image or the final remainder.
func (b *Bot) handleTikTokPhotos(ctx context.Context, userID int64, chatKind string, chatID int64, replyID int, parsed link.Parsed) {
	started := time.Now()
	status := "internal_error"
	cacheKind := "none"
	defer func() {
		if userID <= 0 {
			return
		}
		event := storage.MediaRequest{
			TelegramUserID: userID,
			Platform:       "tiktok", ChatKind: chatKind, Status: status,
			CacheKind: cacheKind, DurationMS: time.Since(started).Milliseconds(),
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := b.store.RecordRequest(saveCtx, event); err != nil {
			b.log.Warn("photo analytics event could not be recorded", zap.Error(err))
		}
	}()
	if !parsed.IsPhoto || parsed.LinkType != link.TypeTikTok {
		return
	}
	sourceKey := "tiktok-photo:" + parsed.VideoID
	release, err := b.downloadGate.begin(userID, chatID, sourceKey, chatKind == "group")
	if err != nil {
		status = "busy"
		if b.downloadGate.shouldNotify(chatID) {
			b.errorWidgetOrText(chatID, replyID, "tiktok", "busy", "⏳ Сейчас много запросов. Попробуй чуть позже.", chatKind == "private")
		}
		return
	}
	defer release()

	// Telegram file_id can be reused without any external fetch or status.
	ids, err := b.store.LookupPhotoAlbum(sourceKey)
	if err == nil {
		if _, sendErr := b.sendPhotoAlbum(chatID, replyID, nil, ids); sendErr == nil {
			cacheKind = "photo"
			status = "success"
		} else {
			status = "send_error"
			b.errorWidgetOrText(chatID, replyID, "tiktok", "send_error", "Не удалось отправить фотографии 😕", chatKind == "private")
		}
		return
	}
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		b.log.Warn("photo cache lookup failed", zap.Error(err))
	}

	releaseQueue, err := b.reserveDownload()
	if err != nil {
		status = "busy"
		if b.downloadGate.shouldNotify(chatID) {
			b.errorWidgetOrText(chatID, replyID, "tiktok", "busy", "⏳ Очередь загрузок занята. Попробуй позже.", chatKind == "private")
		}
		return
	}
	defer releaseQueue()

	// In groups this is ALWAYS the old "⏳ сек, качаю" status, later deleted.
	// Private chats may display the existing widget with indeterminate progress.
	progress := b.beginPhotoStatus(ctx, chatKind, chatID, replyID)
	defer func() { progress.finish(status == "success") }()
	progress.stage("downloading")
	var photos *download.PhotoResult
	err = b.stagedDownload(ctx, func() error {
		jobCtx, cancel := context.WithTimeout(ctx, b.cfg.DownloadExecutionTimeout)
		defer cancel()
		var fetchErr error
		photos, fetchErr = download.DownloadTikTokPhotos(jobCtx, parsed.Raw, b.cfg.Proxy)
		return fetchErr
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrDownloadBusy):
			status = "busy"
			progress.showError("busy", "⏳ Все слоты заняты. Попробуй позже.")
		case errors.Is(err, download.ErrPhotoTooMany):
			status = "too_many_photos"
			progress.showError("too_many_photos", "Слишком много фотографий в публикации.")
		case errors.Is(err, download.ErrPhotoTooLarge):
			status = "too_large"
			progress.showError("photo_too_large", "Фотографии слишком большие для Telegram (10 МиБ/фото, 80 МиБ/пост).")
		case errors.Is(err, download.ErrPhotoBadFormat):
			status = "unsupported"
			progress.showError("photo_unavailable", "Не удалось обработать фотографии TikTok.")
		default:
			status = "photo_unavailable"
			progress.showError("photo_unavailable", "Не удалось получить фотографии TikTok. Попробуй другую публичную ссылку.")
		}
		return
	}
	defer photos.Cleanup()
	progress.stage("preparing")
	progress.stage("sending")
	fileIDs, sendErr := b.sendPhotoAlbum(chatID, replyID, photos.Files, nil)
	if sendErr != nil {
		status = "send_error"
		progress.showError("send_error", "Не удалось отправить фотографии 😕")
		b.log.Warn("TikTok photo album send failed", zap.Error(sendErr))
		return
	}
	status = "success"
	if err := b.store.SavePhotoAlbum(sourceKey, fileIDs); err != nil {
		b.log.Warn("TikTok photo album cache update failed", zap.Error(err))
	}
}

// sendPhotoAlbum is shared by freshly downloaded photos and cached Telegram
// file_ids. No retry for ambiguous server errors; 429 retry is in Sender.
func (b *Bot) sendPhotoAlbum(chatID int64, replyID int, paths, cachedIDs []string) ([]string, error) {
	count := len(paths)
	cached := len(cachedIDs) > 0
	if cached {
		count = len(cachedIDs)
	}
	if count == 0 || count > download.MaxTikTokPhotos {
		return nil, errors.New("invalid photo album count")
	}
	result := make([]string, 0, count)
	for start := 0; start < count; {
		end := min(start+10, count)
		var media []interface{}
		for i := start; i < end; i++ {
			var file tgbotapi.RequestFileData
			if cached {
				file = tgbotapi.FileID(cachedIDs[i])
			} else {
				data, err := os.ReadFile(paths[i])
				if err != nil {
					return nil, err
				}
				file = tgbotapi.FileBytes{
					Name:  filepath.Base(paths[i]),
					Bytes: data,
				}
			}
			item := tgbotapi.NewInputMediaPhoto(file)
			if start == 0 && i == 0 {
				item.Caption = photoCaption
			}
			media = append(media, item)
		}
		if len(media) == 1 {
			photo := tgbotapi.NewPhoto(chatID, media[0].(tgbotapi.InputMediaPhoto).Media)
			if start == 0 {
				photo.Caption = photoCaption
			}
			if start == 0 {
				setReply(&photo.BaseChat, replyID)
			}
			msg, err := b.sender.SendWithResponse(photo)
			if err != nil {
				return nil, fmt.Errorf("sendPhoto failed: %w", err)
			}
			if msg == nil {
				return nil, errors.New("Telegram did not return photo message")
			}
			id, err := bestPhotoFileID(*msg)
			if err != nil {
				return nil, err
			}
			result = append(result, id)
		} else {
			group := tgbotapi.NewMediaGroup(chatID, media)
			if start == 0 {
				group.ReplyToMessageID = replyID
			}
			messages, err := b.sender.SendPhotosGroup(group)
			if err != nil {
				return nil, err
			}
			if len(messages) != len(media) {
				return nil, errors.New("Telegram album returned wrong photo count")
			}
			for _, msg := range messages {
				id, err := bestPhotoFileID(msg)
				if err != nil {
					return nil, err
				}
				result = append(result, id)
			}
		}
		start = end
	}
	return result, nil
}

func bestPhotoFileID(msg tgbotapi.Message) (string, error) {
	if len(msg.Photo) == 0 {
		return "", errors.New("Telegram response contained no photo file IDs")
	}
	id := msg.Photo[len(msg.Photo)-1].FileID
	if id == "" {
		return "", errors.New("Telegram returned empty photo file ID")
	}
	return id, nil
}
