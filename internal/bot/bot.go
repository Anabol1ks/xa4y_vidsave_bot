package bot

import (
	"context"
	"strings"
	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/storage"

	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot — основная структура бота.
type Bot struct {
	api    *tgbotapi.BotAPI
	cfg    *config.Config
	log    *zap.Logger
	sender *Sender
	store  *storage.Storage
}

// New создаёт экземпляр бота.
func New(cfg *config.Config, log *zap.Logger, store *storage.Storage) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}
	log.Info("bot authorized", zap.String("username", api.Self.UserName))

	return &Bot{
		api:    api,
		cfg:    cfg,
		log:    log,
		sender: NewSender(api, log),
		store:  store,
	}, nil
}

// Run запускает long-polling обработку обновлений.
// Блокирует до отмены ctx.
func (b *Bot) Run(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := b.api.GetUpdatesChan(u)

	b.log.Info("bot started, waiting for updates...")
	for {
		select {
		case <-ctx.Done():
			b.log.Info("shutting down gracefully")
			return
		case upd := <-updates:
			b.handleUpdate(ctx, upd)
		}
	}
}

// handleUpdate обрабатывает одно обновление (сообщение пользователя).
func (b *Bot) handleUpdate(ctx context.Context, upd tgbotapi.Update) {
	// Inline-запросы (кнопка «Поделиться»)
	if upd.InlineQuery != nil {
		b.handleInlineQuery(upd.InlineQuery)
		return
	}

	if upd.Message == nil {
		return
	}

	msg := upd.Message
	chatID := msg.Chat.ID

	// Защита от паники в хендлерах
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in handler", zap.Any("recover", r), zap.Int64("chat_id", chatID))
			b.sender.Text(chatID, "что-то сломалось 😵 попробуй позже")
		}
	}()

	// Команды
	if msg.IsCommand() {
		b.handleCommand(chatID, msg)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		b.sender.Text(chatID, "кинь ссылку текстом 👇")
		return
	}

	parsed, err := link.Parse(text, b.cfg.AllowedHosts)
	if err != nil {
		b.handleParseError(chatID, text, err)
		return
	}

	b.log.Info("link accepted",
		zap.String("type", string(parsed.LinkType)),
		zap.String("video_id", parsed.VideoID),
		zap.String("host", parsed.Host),
	)

	switch parsed.LinkType {
	case link.TypeInstagram, link.TypeTikTok:
		b.handleDownload(ctx, chatID, parsed)
	default:
		b.sender.Text(chatID, "этот тип пока не поддерживаю 😕")
	}
}
