package bot

import (
	"context"
	"sync/atomic"
	"time"

	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

const (
	downloadWidgetTick        = 350 * time.Millisecond
	downloadWidgetMinInterval = 3 * time.Second
	downloadWidgetSuccessHold = 6 * time.Second
)

// Presentation only. If widget API fails, the original text status is used.
// No widget action can start a new download; no callback buttons are present.
type downloadStatus struct {
	b           *Bot
	chatID      int64
	textMessage *tgbotapi.Message
	widget      *downloadWidgetSession
}

type downloadWidgetSession struct {
	b         *Bot
	chatID    int64
	messageID int
	platform  string
	updates   chan downloadWidgetData
	complete  chan bool
	revision  atomic.Int64
}

func (b *Bot) beginDownloadStatus(ctx context.Context, chatKind string, chatID int64, replyID int, platform link.Type) *downloadStatus {
	s := &downloadStatus{b: b, chatID: chatID}
	if b.downloadWidgetActive(chatID) && chatKind == "private" && b.downloadWidgetAPI != nil {
		messageID, err := b.downloadWidgetAPI.send(ctx, chatID, replyID, string(platform))
		if err == nil {
			w := &downloadWidgetSession{
				b: b, chatID: chatID, messageID: messageID, platform: string(platform),
				updates: make(chan downloadWidgetData, 1), complete: make(chan bool, 1),
			}
			w.revision.Store(time.Now().UnixMicro())
			s.widget = w
			go w.run()
			return s
		}
		b.log.Warn("download widget failed; using legacy text status", zap.Error(err))
	}
	s.textMessage = b.sender.TextWithResponseReply(chatID, replyID, "⏳ сек, качаю")
	return s
}

func (s *downloadStatus) stage(stage string) {
	if s != nil && s.widget != nil {
		s.widget.push(stage, nil)
	}
}
func (s *downloadStatus) progress(p download.ProgressEvent) {
	if s == nil || s.widget == nil {
		return
	}
	if p.Known {
		value := p.Percent
		s.widget.push("downloading", &value)
	} else {
		s.widget.push("downloading", nil)
	}
}

func (s *downloadStatus) finish(success bool) {
	if s == nil {
		return
	}
	if s.widget != nil {
		select {
		case s.widget.complete <- success:
		default:
		}
		return
	}
	if s.textMessage != nil {
		s.b.sender.Delete(s.chatID, s.textMessage.MessageID)
	}
}

func (w *downloadWidgetSession) push(stage string, percent *int) {
	data := downloadWidgetData{Platform: w.platform, Stage: stage, Percent: percent, Revision: w.revision.Add(1)}
	// Always publish latest state; never block yt-dlp pipe on Telegram networking.
	select {
	case w.updates <- data:
	default:
		select {
		case <-w.updates:
		default:
		}
		select {
		case w.updates <- data:
		default:
		}
	}
}

func (w *downloadWidgetSession) run() {
	ticker := time.NewTicker(downloadWidgetTick)
	defer ticker.Stop()
	var pending *downloadWidgetData
	var lastSent downloadWidgetData
	var lastUpdate time.Time
	failures := 0
	for {
		select {
		case state := <-w.updates:
			pending = &state
		case <-ticker.C:
			if pending == nil || failures >= 2 {
				continue
			}
			changedStage := lastSent.Stage != pending.Stage
			percentageMoved := pending.Percent != nil && (lastSent.Percent == nil || *pending.Percent >= *lastSent.Percent+5 || *pending.Percent < *lastSent.Percent)
			if !changedStage && !percentageMoved {
				continue
			}
			if time.Since(lastUpdate) < downloadWidgetMinInterval && !changedStage {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			err := w.b.downloadWidgetAPI.edit(ctx, w.chatID, w.messageID, *pending)
			cancel()
			lastUpdate = time.Now()
			if err != nil {
				failures++
				w.b.log.Warn("live widget edit failed; download continues", zap.Error(err))
			} else {
				failures = 0
				lastSent = *pending
			}
			pending = nil
		case success := <-w.complete:
			if success && failures < 2 {
				pct := 100
				final := downloadWidgetData{Platform: w.platform, Stage: "success", Percent: &pct, Revision: w.revision.Add(1)}
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				err := w.b.downloadWidgetAPI.edit(ctx, w.chatID, w.messageID, final)
				cancel()
				if err != nil {
					w.b.log.Warn("live widget final edit failed; video was sent", zap.Error(err))
				}
				// Delete only service widget, never the downloaded video.
				go func() {
					timer := time.NewTimer(downloadWidgetSuccessHold)
					defer timer.Stop()
					<-timer.C
					w.b.sender.Delete(w.chatID, w.messageID)
				}()
			} else {
				// Stage 4 will implement error cards. Existing error text is retained.
				w.b.sender.Delete(w.chatID, w.messageID)
			}
			return
		}
	}
}
