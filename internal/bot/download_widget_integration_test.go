package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

type fakeVideoSender struct {
	mu       sync.Mutex
	videos   int
	messages int
	deletes  int
}

func (s *fakeVideoSender) Send(value tgbotapi.Chattable) (tgbotapi.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch value.(type) {
	case tgbotapi.VideoConfig:
		s.videos++
		return tgbotapi.Message{
			MessageID: s.videos + 200,
			Video:     &tgbotapi.Video{FileID: "stage3_cached_file_id", FileUniqueID: "stage3_unique_id"},
		}, nil
	default:
		s.messages++
		return tgbotapi.Message{MessageID: s.messages + 20}, nil
	}
}

func (s *fakeVideoSender) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	s.mu.Lock()
	s.deletes++
	s.mu.Unlock()
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (s *fakeVideoSender) count() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.videos, s.messages
}

// Exercises the whole existing SHA/cache/send path against a disposable CI
// PostgreSQL, fake yt-dlp executable and local mock Telegram API; no production
// tokens, accounts, external networks or real videos are involved.
func TestDownloadWidgetEndToEndPostgres(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL database")
	}
	store, err := storage.New(dsn, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	script := `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-o" ]; then shift; out="$1"; fi
 shift
done
file=$(printf '%s' "$out" | sed 's/%(ext)s/mp4/g')
printf 'stage3-test-video-data' > "$file"
printf 'VIDSAVE_PROGRESS|downloading|5|10|NA\n' >&2
printf 'VIDSAVE_PROGRESS|downloading|10|10|NA\n' >&2
printf '%s\n' "$file"
`
	if err := os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mock := &widgetServer{}
	server := mock.endpoint(t)
	defer server.Close()
	sender := &fakeVideoSender{}
	cfg := &config.Config{
		DownloadWidgetsEnabled:   true,
		MaxWaitingDownloads:      2,
		DownloadQueueTimeout:     time.Second,
		DownloadExecutionTimeout: 5 * time.Second,
		DownloadUserCooldown:     time.Second,
		DownloadSourceCooldown:   30 * time.Second,
		DownloadGroupPerMinute:   10,
		MaxDownloadBytes:         50 * 1024 * 1024,
	}
	api := &downloadWidgetAPI{endpoint: server.URL, client: server.Client()}
	b := &Bot{
		cfg: cfg, log: zap.NewNop(), store: store,
		sender:            &Sender{api: sender, log: zap.NewNop(), sleep: func(time.Duration) {}},
		downloadWidgetAPI: api,
		downloadGate:      newDownloadGate(cfg),
		downloadSlots:     make(chan struct{}, 1),
		downloadWaitSlots: make(chan struct{}, 2),
	}
	id := time.Now().UnixNano()
	parsed := link.Parsed{
		LinkType: link.TypeTikTok,
		VideoID:  "stage3-test-" + strconv.FormatInt(id, 10),
		Raw:      "https://www.tiktok.com/@test/video/123",
	}
	b.handleDownload(context.Background(), id, "private", id, 0, parsed)
	videos, messages := sender.count()
	if videos != 1 || messages != 0 {
		t.Fatalf("video sent=%d text messages=%d", videos, messages)
	}
	if len(mock.callsFor("sendWidget")) != 1 {
		t.Fatal("expected live widget")
	}
	// Internal cache entry is present after the successful real sending pipeline.
	key := storage.SourceKeyFromParsed("tiktok", parsed.VideoID)
	cached, err := store.Lookup(key)
	if err != nil || cached.TgFileID == "" {
		t.Fatalf("source cache not saved: %+v %v", cached, err)
	}
	// A different source can be sent from cache and MUST NOT start a widget.
	time.Sleep(1100 * time.Millisecond)
	b.handleDownload(context.Background(), id, "private", id, 0, parsed)
	videos, _ = sender.count()
	if videos != 1 {
		t.Fatal("source cooldown didn't reject duplicate", videos)
	}
	// The response to same source within the cooldown is a short informational message.
	if len(mock.callsFor("sendWidget")) != 1 {
		t.Fatal("duplicate widget created")
	}
	// Inspect only aggregate widget state, never private links or user IDs.
	for _, call := range mock.callsFor("sendWidget") {
		var state map[string]interface{}
		if err := json.Unmarshal([]byte(call.params.Get("chat_data")), &state); err != nil {
			t.Fatal(err)
		}
		if state["p"] != "tiktok" || state["s"] != "validating" {
			t.Fatal(state)
		}
		if strings.Contains(call.params.Get("chat_data"), parsed.Raw) {
			t.Fatal("raw link leaked")
		}
	}
}
