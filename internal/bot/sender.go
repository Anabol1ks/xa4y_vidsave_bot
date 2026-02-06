package bot

import (
	"strings"
	"time"

	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Sender — обёртка над bot.Send с обработкой rate-limit (429).
type Sender struct {
	api *tgbotapi.BotAPI
	log *zap.Logger
}

func NewSender(api *tgbotapi.BotAPI, log *zap.Logger) *Sender {
	return &Sender{api: api, log: log}
}

// maxRetries — сколько раз повторяем при 429.
const maxRetries = 3

// Send отправляет Chattable (видео, фото, текст и т.д.) с retry при 429.
func (s *Sender) Send(c tgbotapi.Chattable) error {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		_, err := s.api.Send(c)
		if err == nil {
			return nil
		}

		// Проверяем 429 Too Many Requests
		if isRateLimited(err) {
			wait := retryAfter(err, attempt)
			s.log.Warn("rate limited by Telegram, waiting",
				zap.Duration("wait", wait),
				zap.Int("attempt", attempt),
			)
			time.Sleep(wait)
			continue
		}

		// Другая ошибка — не ретраим
		return err
	}

	return nil
}

// Text — удобная обёртка для отправки текстового сообщения.
func (s *Sender) Text(chatID int64, text string) {
	if err := s.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		s.log.Warn("failed to send text message",
			zap.Error(err),
			zap.Int64("chat_id", chatID),
		)
	}
}

// isRateLimited проверяет, является ли ошибка 429 (Too Many Requests).
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "429") || strings.Contains(msg, "Too Many Requests") || strings.Contains(msg, "retry after")
}

// retryAfter определяет время ожидания перед повторной отправкой.
// Telegram обычно присылает retry_after в ошибке, но для простоты
// используем экспоненциальный backoff.
func retryAfter(_ error, attempt int) time.Duration {
	switch attempt {
	case 1:
		return 3 * time.Second
	case 2:
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}
