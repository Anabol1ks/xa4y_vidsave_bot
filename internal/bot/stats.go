package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xa4yy_vidsave/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

func statsAuthorized(msg *tgbotapi.Message, allowed map[int64]struct{}) bool {
	if msg == nil || msg.From == nil || msg.Chat == nil || !msg.Chat.IsPrivate() {
		return false
	}
	if msg.From.ID <= 0 || msg.Chat.ID != msg.From.ID {
		return false
	}
	_, ok := allowed[msg.From.ID]
	return ok
}

func (b *Bot) trackUser(parent context.Context, user *tgbotapi.User) {
	if user == nil || user.IsBot || user.ID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if err := b.store.TouchUser(ctx, user.ID); err != nil {
		b.log.Warn("user activity tracking failed", zap.Error(err))
	}
}

func (b *Bot) installCommandMenu() {
	public := []tgbotapi.BotCommand{
		{Command: "start", Description: "Начать работу"},
		{Command: "help", Description: "Как скачать видео"},
		{Command: "id", Description: "Узнать свой Telegram ID"},
	}
	if _, err := b.api.Request(tgbotapi.NewSetMyCommands(public...)); err != nil {
		b.log.Warn("cannot update public command menu", zap.Error(err))
	}
	for id := range b.cfg.AdminTelegramIDs {
		b.installOwnerCommandMenu(id, public)
	}
}

func (b *Bot) installOwnerCommandMenu(id int64, public []tgbotapi.BotCommand) {
	commands := append([]tgbotapi.BotCommand(nil), public...)
	commands = append(commands, tgbotapi.BotCommand{Command: "stats", Description: "Закрытая статистика"})
	scope := tgbotapi.NewBotCommandScopeChat(id)
	if _, err := b.api.Request(tgbotapi.NewSetMyCommandsWithScope(scope, commands...)); err != nil {
		b.log.Warn("cannot update owner command menu", zap.Int64("owner_id", id), zap.Error(err))
	}
}

func successRate(success, total int64) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(success)/float64(total))
}

func formatStats(a storage.AnalyticsSnapshot) string {
	var out strings.Builder
	fmt.Fprintf(&out,
		"📊 Статистика бота\n\n"+
			"👥 Пользователи (с запуска учёта)\n"+
			"Всего: %d · Новые за 24 ч: %d\n"+
			"DAU (24 ч): %d · WAU (7 д): %d · MAU (30 д): %d\n\n"+
			"🎬 Запросы (с запуска учёта)\n"+
			"Всего: %d · Успешно: %d · Ошибок: %d\n"+
			"Успешность: %s\n"+
			"Из кэша Telegram: %d · По SHA256: %d\n\n"+
			"📆 24 ч: %d / %d (%s)\n"+
			"7 д: %d / %d (%s)\n"+
			"30 д: %d / %d (%s)\n\n"+
			"💾 Исторический кэш: %d ссылок, %d повторных обращений\n",
		a.Users, a.NewUsers24h, a.DAU, a.WAU, a.MAU,
		a.Requests, a.Success, a.Failed, successRate(a.Success, a.Requests),
		a.SourceCache, a.HashCache,
		a.Success24h, a.Requests24h, successRate(a.Success24h, a.Requests24h),
		a.Success7d, a.Requests7d, successRate(a.Success7d, a.Requests7d),
		a.Success30d, a.Requests30d, successRate(a.Success30d, a.Requests30d),
		a.CachedLinks, a.LegacyCacheHit)
	if len(a.Platforms) != 0 {
		out.WriteString("\n🌐 Платформы (успешно / запросов):\n")
		for _, item := range a.Platforms {
			fmt.Fprintf(&out, "%s: %d / %d\n", item.Platform, item.Success, item.Requests)
		}
	}
	if len(a.Errors) != 0 {
		out.WriteString("\n⚠️ Ошибки:\n")
		for _, item := range a.Errors {
			fmt.Fprintf(&out, "%s: %d\n", item.Status, item.Count)
		}
	}
	out.WriteString("\nℹ️ До включения аналитики пользователи и точное количество запросов не учитывались.")
	return out.String()
}
