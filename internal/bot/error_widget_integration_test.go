package bot

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/link"
	"xa4yy_vidsave/internal/storage"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Full pipeline: source validation -> yt-dlp failure -> error-card send ->
// progress status cleanup -> one analytics event. Everything uses a disposable
// PostgreSQL schema, a fake executable and a fake Telegram Bot API.
func TestErrorWidgetEndToEndPostgres(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL test database")
	}
	parsedDSN, err := url.Parse(dsn)
	if err != nil || !strings.Contains(parsedDSN.Path, "test") {
		t.Fatal("refusing to use a non-test database")
	}
	schema := "vidsave_stage4_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	rootDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := rootDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := rootDB.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer rootDB.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := parsedDSN.Query()
	q.Set("search_path", schema)
	parsedDSN.RawQuery = q.Encode()
	store, err := storage.New(parsedDSN.String(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	execDir := t.TempDir()
	script := `#!/bin/sh
last=""
for x in "$@"; do last="$x"; done
case "$last" in
 *instagram*) echo "ERROR: This account is private" >&2 ;;
 *tiktok*) echo "ERROR: HTTP Error 429: Too Many Requests" >&2 ;;
 *youtube*) echo "ERROR: socket timed out" >&2 ;;
 *) echo "ERROR: unexpected URL" >&2 ;;
esac
exit 1
`
	if err := os.WriteFile(filepath.Join(execDir, "yt-dlp"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", execDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fake := &widgetServer{}
	srv := fake.endpoint(t)
	defer srv.Close()
	sender := &fakeVideoSender{}
	cfg := &config.Config{
		DownloadWidgetsEnabled: true, ErrorWidgetsEnabled: true,
		MaxDownloadBytes:         50 * 1024 * 1024,
		DownloadQueueTimeout:     time.Second,
		DownloadExecutionTimeout: 5 * time.Second,
		DownloadUserCooldown:     time.Second, DownloadSourceCooldown: time.Second,
	}
	b := &Bot{
		cfg: cfg, store: store, log: zap.NewNop(),
		sender:            &Sender{api: sender, log: zap.NewNop(), sleep: func(time.Duration) {}},
		downloadWidgetAPI: &downloadWidgetAPI{endpoint: srv.URL, client: srv.Client()},
		downloadGate:      newDownloadGate(cfg),
		downloadSlots:     make(chan struct{}, 1),
		downloadWaitSlots: make(chan struct{}, 2),
	}
	testCases := []struct {
		p         link.Type
		url, code string
	}{
		{link.TypeInstagram, "https://www.instagram.com/reel/abc", "private_video"},
		{link.TypeTikTok, "https://www.tiktok.com/@user/video/12", "source_rate_limited"},
		{link.TypeYouTube, "https://www.youtube.com/shorts/abc", "network_error"},
	}
	baseID := time.Now().UnixNano()
	for i, tc := range testCases {
		userID := baseID + int64(i) + 1
		b.handleDownload(context.Background(), userID, "private", userID, 0, link.Parsed{
			LinkType: tc.p, Raw: tc.url, VideoID: "stage4-" + strconv.FormatInt(userID, 10),
		})
	}
	// Two widgets per failed download: transient progress + durable error card.
	sends := fake.callsFor("sendWidget")
	if len(sends) != 6 {
		t.Fatalf("expected 6 sends, got %d", len(sends))
	}
	for i, tc := range testCases {
		var progress map[string]interface{}
		if err := json.Unmarshal([]byte(sends[2*i].params.Get("chat_data")), &progress); err != nil {
			t.Fatal(err)
		}
		if progress["s"] != "validating" {
			t.Fatal("missing initial progress widget", progress)
		}
		var final errorWidgetState
		if err := json.Unmarshal([]byte(sends[2*i+1].params.Get("chat_data")), &final); err != nil {
			t.Fatal(err)
		}
		if final.Platform != string(tc.p) || final.Code != tc.code {
			t.Fatalf("%s: %+v", tc.p, final)
		}
	}
	videos, messages := sender.count()
	if videos != 0 || messages != 0 {
		t.Fatalf("duplicate error text/video: videos %d, messages %d", videos, messages)
	}
	report, err := store.Analytics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 3 || report.Success != 0 || report.Failed != 3 {
		t.Fatalf("events must not double-count widget notifications: %+v", report)
	}
}
