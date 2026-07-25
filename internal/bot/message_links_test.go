package bot

import (
	"testing"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestFirstSupportedLink(t *testing.T) {
	instagramURL := "https://www.instagram.com/reel/DbJLODStVAd/?igsh=test"
	tikTokURL := "https://www.tiktok.com/@user/video/1234567890"

	tests := []struct {
		name      string
		message   *tgbotapi.Message
		wantType  string
		wantID    string
		wantFound bool
	}{
		{
			name:      "embedded Instagram URL",
			message:   &tgbotapi.Message{Text: "смотри " + instagramURL + " 🔥"},
			wantType:  "instagram",
			wantID:    "DbJLODStVAd",
			wantFound: true,
		},
		{
			name:      "TikTok URL in caption",
			message:   &tgbotapi.Message{Caption: "видео: " + tikTokURL},
			wantType:  "tiktok",
			wantID:    "1234567890",
			wantFound: true,
		},
		{
			name: "hidden text link",
			message: &tgbotapi.Message{
				Text: "открыть видео",
				Entities: []tgbotapi.MessageEntity{{
					Type:   "text_link",
					Offset: 0,
					Length: 13,
					URL:    instagramURL,
				}},
			},
			wantType:  "instagram",
			wantID:    "DbJLODStVAd",
			wantFound: true,
		},
		{
			name: "visible URL entity after emoji uses UTF-16 offsets",
			message: &tgbotapi.Message{
				Text: "🔥 " + tikTokURL,
				Entities: []tgbotapi.MessageEntity{{
					Type:   "url",
					Offset: 3,
					Length: utf16Length(tikTokURL),
				}},
			},
			wantType:  "tiktok",
			wantID:    "1234567890",
			wantFound: true,
		},
		{
			name:      "skips unsupported URL before supported URL",
			message:   &tgbotapi.Message{Text: "https://example.com/video " + instagramURL},
			wantType:  "instagram",
			wantID:    "DbJLODStVAd",
			wantFound: true,
		},
		{
			name:      "trims sentence punctuation",
			message:   &tgbotapi.Message{Text: "держи (" + instagramURL + ")."},
			wantType:  "instagram",
			wantID:    "DbJLODStVAd",
			wantFound: true,
		},
		{
			name:      "ignores ordinary conversation",
			message:   &tgbotapi.Message{Text: "обычное сообщение без ссылки"},
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, found := firstSupportedLink(tt.message, nil)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if string(parsed.LinkType) != tt.wantType {
				t.Errorf("type = %q, want %q", parsed.LinkType, tt.wantType)
			}
			if parsed.VideoID != tt.wantID {
				t.Errorf("video ID = %q, want %q", parsed.VideoID, tt.wantID)
			}
		})
	}
}

func TestUTF16SliceRejectsInvalidRange(t *testing.T) {
	if got := utf16Slice("тест", -1, 2); got != "" {
		t.Fatalf("utf16Slice() = %q, want empty string", got)
	}
}

func TestSetReply(t *testing.T) {
	base := tgbotapi.BaseChat{}
	setReply(&base, 42)

	if base.ReplyToMessageID != 42 {
		t.Errorf("ReplyToMessageID = %d, want 42", base.ReplyToMessageID)
	}
	if !base.AllowSendingWithoutReply {
		t.Error("AllowSendingWithoutReply = false, want true")
	}
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}
