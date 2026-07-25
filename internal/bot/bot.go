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
	api           *tgbotapi.BotAPI
	cfg           *config.Config
	log           *zap.Logger
	sender        *Sender
	store         *storage.Storage
	downloadSlots chan struct{}
}

// New создаёт экземпляр бота.
func New(cfg *config.Config, log *zap.Logger, store *storage.Storage) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}

	maxConcurrentDownloads := cfg.MaxConcurrentDownloads
	if maxConcurrentDownloads < 1 {
		maxConcurrentDownloads = 3
	}

	log.Info("bot authorized",
		zap.String("username", api.Self.UserName),
		zap.Bool("can_read_all_group_messages", api.Self.CanReadAllGroupMessages),
		zap.Int("max_concurrent_downloads", maxConcurrentDownloads),
	)
	if !api.Self.CanReadAllGroupMessages {
		log.Warn("Telegram privacy mode is enabled; disable it via BotFather /setprivacy to receive ordinary group messages")
	}

	return &Bot{
		api:           api,
		cfg:           cfg,
		log:           log,
		sender:        NewSender(api, log),
		store:         store,
		downloadSlots: make(chan struct{}, maxConcurrentDownloads),
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
			go b.handleUpdate(ctx, upd)
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
	isPrivate := msg.Chat.IsPrivate()
	isGroup := msg.Chat.IsGroup() || msg.Chat.IsSuperGroup()
	if !isPrivate && !isGroup {
		return
	}

	replyToMessageID := 0
	if isGroup {
		replyToMessageID = msg.MessageID
	}

	// Защита от паники в хендлерах
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in handler", zap.Any("recover", r), zap.Int64("chat_id", chatID))
			b.sender.TextReply(chatID, replyToMessageID, "что-то сломалось 😵 попробуй позже")
		}
	}()

	// Не реагируем на сообщения других ботов, чтобы избежать циклов в группах.
	if msg.From != nil && msg.From.IsBot {
		return
	}

	// Команды
	if msg.IsCommand() {
		b.handleCommand(chatID, msg)
		return
	}

	parsed, ok := firstSupportedLink(msg, b.cfg.AllowedHosts)
	if !ok {
		// В группах обычные сообщения и неподдерживаемые ссылки игнорируются без шума.
		if isGroup {
			return
		}

		text := strings.TrimSpace(messageText(msg))
		if text == "" {
			b.sender.Text(chatID, "кинь ссылку текстом 👇")
			return
		}

		_, err := link.Parse(text, b.cfg.AllowedHosts)
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
		b.handleDownload(ctx, chatID, replyToMessageID, parsed)
	default:
		b.sender.TextReply(chatID, replyToMessageID, "этот тип пока не поддерживаю 😕")
	}
}
