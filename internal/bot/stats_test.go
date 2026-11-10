package bot

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"xa4yy_vidsave/internal/storage"
)

func TestStatsAuthorization(t *testing.T) {
	allow := map[int64]struct{}{123: {}}
	cases := []struct {
		name string
		msg  *tgbotapi.Message
		want bool
	}{
		{"owner private", &tgbotapi.Message{From: &tgbotapi.User{ID: 123}, Chat: &tgbotapi.Chat{ID: 123, Type: "private"}}, true},
		{"stranger private", &tgbotapi.Message{From: &tgbotapi.User{ID: 124}, Chat: &tgbotapi.Chat{ID: 124, Type: "private"}}, false},
		{"owner in group", &tgbotapi.Message{From: &tgbotapi.User{ID: 123}, Chat: &tgbotapi.Chat{ID: -789, Type: "supergroup"}}, false},
		{"forged private chat", &tgbotapi.Message{From: &tgbotapi.User{ID: 123}, Chat: &tgbotapi.Chat{ID: 124, Type: "private"}}, false},
		{"missing sender", &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 123, Type: "private"}}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statsAuthorized(tc.msg, allow); got != tc.want {
				t.Errorf("got %t, want %t", got, tc.want)
			}
		})
	}
	if statsAuthorized(cases[0].msg, nil) {
		t.Fatal("nil allowlist must deny everyone")
	}
}

func TestStatsFormatting(t *testing.T) {
	stats := storage.AnalyticsSnapshot{
		Users: 10, NewUsers24h: 2, DAU: 3, WAU: 5, MAU: 8,
		Requests: 11, Success: 9, Failed: 2,
		CachedLinks: 434, Platforms: []storage.PlatformStat{{Platform: "youtube", Requests: 4, Success: 3}},
		Errors: []storage.ErrorStat{{Status: "auth_required", Count: 2}},
	}
	text := formatStats(stats)
	for _, want := range []string{"434", "DAU", "Успешно: 9", "81.8%", "youtube", "auth_required"} {
		if !strings.Contains(text, want) {
			t.Errorf("formatStats missing %q", want)
		}
	}
	if successRate(0, 0) != "—" {
		t.Fatal("division by zero should show dash")
	}
}
