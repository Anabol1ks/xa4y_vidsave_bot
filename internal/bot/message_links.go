package bot

import (
	"regexp"
	"strings"
	"unicode/utf16"
	"xa4yy_vidsave/internal/link"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var httpURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// firstSupportedLink возвращает первую поддерживаемую ссылку из текста или подписи.
// Telegram entities проверяются первыми, чтобы поддержать скрытые text_link.
func firstSupportedLink(msg *tgbotapi.Message, allowedHosts map[string]struct{}) (link.Parsed, bool) {
	parts := []struct {
		text     string
		entities []tgbotapi.MessageEntity
	}{
		{text: msg.Text, entities: msg.Entities},
		{text: msg.Caption, entities: msg.CaptionEntities},
	}

	for _, part := range parts {
		for _, entity := range part.entities {
			var candidate string
			switch {
			case entity.IsTextLink():
				candidate = entity.URL
			case entity.IsURL():
				candidate = utf16Slice(part.text, entity.Offset, entity.Length)
			default:
				continue
			}

			if parsed, ok := parseSupportedCandidate(candidate, allowedHosts); ok {
				return parsed, true
			}
		}

		// Fallback нужен для тестов, клиентов без entities и текста с URL,
		// который Telegram по какой-либо причине не разметил.
		for _, candidate := range httpURLPattern.FindAllString(part.text, -1) {
			if parsed, ok := parseSupportedCandidate(candidate, allowedHosts); ok {
				return parsed, true
			}
		}
	}

	return link.Parsed{}, false
}

func parseSupportedCandidate(candidate string, allowedHosts map[string]struct{}) (link.Parsed, bool) {
	candidate = strings.TrimSpace(candidate)
	candidate = strings.Trim(candidate, "<>\"'")
	candidate = strings.TrimRight(candidate, ".,!?;:)]}")

	parsed, err := link.Parse(candidate, allowedHosts)
	if err != nil {
		return link.Parsed{}, false
	}
	return parsed, true
}

func messageText(msg *tgbotapi.Message) string {
	if strings.TrimSpace(msg.Text) != "" {
		return msg.Text
	}
	return msg.Caption
}

// Telegram задаёт offsets entities в UTF-16 code units, а Go хранит UTF-8.
func utf16Slice(text string, offset int, length int) string {
	units := utf16.Encode([]rune(text))
	if offset < 0 || length <= 0 || offset > len(units) || offset+length > len(units) {
		return ""
	}
	return string(utf16.Decode(units[offset : offset+length]))
}
