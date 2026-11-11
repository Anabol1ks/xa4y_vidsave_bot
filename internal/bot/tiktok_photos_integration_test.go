package bot

import (
	"context"
	"errors"
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

// End-to-end without external TikTok/Telegram connections: fake gallery-dl,
// mocked Telegram media group API, isolated disposable PostgreSQL schema.
// Verifies old group status is deleted, cache reuses FileIDs, and analytics
// counts only requests (not each photo).
func TestTikTokPhotoGroupEndToEndPostgres(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL test database")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.Contains(u.Path, "test") {
		t.Fatal("refusing to write to non-test database")
	}
	schema := "vidsave_tiktok_photo_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer root.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, err := storage.New(u.String(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	execDir := t.TempDir()
	extractorCalls := filepath.Join(execDir, "calls")
	script := `#!/bin/sh
dir=""
while [ "$#" -gt 0 ]; do
 if [ "$1" = "--directory" ]; then shift; dir="$1"; fi
 shift
done
printf 'called\\n' >> "` + extractorCalls + `"
printf 'fake-photo-one' > "$dir/01.jpg"
printf 'fake-photo-two' > "$dir/02.jpg"
printf 'fake-photo-three' > "$dir/03.jpg"
`
	if err := os.WriteFile(filepath.Join(execDir, "gallery-dl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", execDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	api := &mockTelegramPhotos{}
	cfg := &config.Config{
		TikTokPhotosEnabled:      true,
		DownloadWidgetsEnabled:   true,
		MaxWaitingDownloads:      2,
		DownloadQueueTimeout:     time.Second,
		DownloadExecutionTimeout: 10 * time.Second,
		DownloadUserCooldown:     time.Second,
		DownloadSourceCooldown:   time.Second,
		DownloadGroupPerMinute:   20,
	}
	b := &Bot{
		cfg: cfg, log: zap.NewNop(), store: store,
		sender:        &Sender{api: api, log: zap.NewNop(), sleep: func(time.Duration) {}},
		downloadGate:  newDownloadGate(cfg),
		downloadSlots: make(chan struct{}, 1), downloadWaitSlots: make(chan struct{}, 3),
	}
	p := link.Parsed{
		Raw:      "https://www.tiktok.com/@isisusisp/photo/7706556456627096852",
		LinkType: link.TypeTikTok, VideoID: "7706556456627096852", IsPhoto: true,
	}
	b.handleTikTokPhotos(context.Background(), 100001, "group", -1234, 700, p)
	if len(api.groupCalls) != 1 || len(api.groupCalls[0]) != 3 {
		t.Fatalf("album not sent in group, groups=%d", len(api.groupCalls))
	}
	if api.textCalls != 1 || api.deleteCalls != 1 {
		t.Fatalf("group temp status must be sent once and deleted: text=%d del=%d", api.textCalls, api.deleteCalls)
	}
	if api.groupReplies[0] != 700 {
		t.Fatal("media group didn't reply to user link")
	}
	ids, err := store.LookupPhotoAlbum("tiktok-photo:" + p.VideoID)
	if err != nil || len(ids) != 3 {
		t.Fatalf("file_ids not cached: %+v %v", ids, err)
	}
	calls, err := os.ReadFile(extractorCalls)
	if err != nil || strings.Count(string(calls), "called") != 1 {
		t.Fatal("first download did not invoke extractor once")
	}
	// Another person can reuse the cached photo album without any extraction.
	b.handleTikTokPhotos(context.Background(), 100002, "group", -1234, 701, p)
	calls, err = os.ReadFile(extractorCalls)
	if err != nil || strings.Count(string(calls), "called") != 1 {
		t.Fatal("cache hit reran gallery-dl")
	}
	if len(api.groupCalls) != 2 {
		t.Fatal("cached photo group not sent")
	}
	if api.textCalls != 1 || api.deleteCalls != 1 {
		t.Fatal("cache hit should not create extra status message")
	}
	// Group replies are from two real users, counted as two requests total.
	report, err := store.Analytics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 || report.Success != 2 || report.Failed != 0 {
		t.Fatalf("photo album counted as multiple downloads or failure: %+v", report)
	}
	if report.CachedLinks != 0 {
		t.Fatal("photo album incorrectly counted as historical video")
	}
	if _, err := store.Lookup("tiktok:7706556456627096852"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("photo poisoned video cache: %v", err)
	}
}
