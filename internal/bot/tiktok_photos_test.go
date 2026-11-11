package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xa4yy_vidsave/internal/config"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

type mockTelegramPhotos struct {
	textCalls     int
	deleteCalls   int
	photoCalls    int
	groupCalls    [][]interface{}
	groupReplies  []int
	groupCaptions []string
	photoReplies  []int
	failGroups    bool
}

func (m *mockTelegramPhotos) Send(item tgbotapi.Chattable) (tgbotapi.Message, error) {
	switch p := item.(type) {
	case tgbotapi.PhotoConfig:
		m.photoCalls++
		m.photoReplies = append(m.photoReplies, p.ReplyToMessageID)
		return tgbotapi.Message{Photo: []tgbotapi.PhotoSize{{FileID: "p-s"}, {FileID: "photo-" + string(rune('0'+m.photoCalls))}}}, nil
	default:
		m.textCalls++
		return tgbotapi.Message{MessageID: 444}, nil
	}
}
func (m *mockTelegramPhotos) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	m.deleteCalls++
	return &tgbotapi.APIResponse{Ok: true}, nil
}
func (m *mockTelegramPhotos) SendMediaGroup(g tgbotapi.MediaGroupConfig) ([]tgbotapi.Message, error) {
	if m.failGroups {
		return nil, errTelegramRateLimit
	}
	m.groupCalls = append(m.groupCalls, g.Media)
	m.groupReplies = append(m.groupReplies, g.ReplyToMessageID)
	var caption string
	if len(g.Media) > 0 {
		caption = g.Media[0].(tgbotapi.InputMediaPhoto).Caption
	}
	m.groupCaptions = append(m.groupCaptions, caption)
	msg := make([]tgbotapi.Message, len(g.Media))
	for i := range msg {
		msg[i].Photo = []tgbotapi.PhotoSize{{FileID: "small"}, {FileID: "group-file-" + string(rune('a'+i))}}
	}
	return msg, nil
}

func photoTestBot(api *mockTelegramPhotos) *Bot {
	return &Bot{sender: &Sender{api: api, log: zap.NewNop(), sleep: func(time.Duration) {}},
		cfg: &config.Config{DownloadWidgetsEnabled: true, TikTokPhotosEnabled: true}, log: zap.NewNop()}
}

func makeFakePhotos(t *testing.T, count int) []string {
	t.Helper()
	root := t.TempDir()
	paths := make([]string, count)
	for i := range paths {
		path := filepath.Join(root, "photo-"+string(rune('a'+i))+".jpg")
		if err := os.WriteFile(path, []byte("test jpeg bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		paths[i] = path
	}
	return paths
}

func TestTikTokPhotoAlbumSingleVsSplit(t *testing.T) {
	for _, test := range []struct{ n, groups, singles int }{
		{1, 0, 1}, {2, 1, 0}, {10, 1, 0}, {11, 1, 1}, {20, 2, 0}, {21, 2, 1}, {35, 4, 0},
	} {
		t.Run(string(rune('a'+test.n)), func(t *testing.T) {
			api := &mockTelegramPhotos{}
			b := photoTestBot(api)
			ids, err := b.sendPhotoAlbum(-444, 189, makeFakePhotos(t, test.n), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != test.n || len(api.groupCalls) != test.groups || api.photoCalls != test.singles {
				t.Fatalf("n=%d id=%d groups=%d singles=%d", test.n, len(ids), len(api.groupCalls), api.photoCalls)
			}
			for i, g := range api.groupCalls {
				if len(g) < 2 || len(g) > 10 {
					t.Fatal("invalid Telegram media group")
				}
				if i == 0 {
					if api.groupReplies[0] != 189 || api.groupCaptions[0] != photoCaption {
						t.Fatal("first media group missing caption/reply")
					}
				} else if api.groupCaptions[i] != "" || api.groupReplies[i] != 0 {
					t.Fatal("followup group must not repeat caption/reply")
				}
			}
		})
	}
}

func TestTikTokPhotoAlbumCacheUsesFileIDs(t *testing.T) {
	api := &mockTelegramPhotos{}
	b := photoTestBot(api)
	ids := []string{"cached-one", "cached-two", "cached-three"}
	got, err := b.sendPhotoAlbum(123, 77, nil, ids)
	if err != nil || len(got) != 3 {
		t.Fatalf("cached album failed: %v %+v", err, got)
	}
	if len(api.groupCalls) != 1 || api.photoCalls != 0 {
		t.Fatal("cached album was not batched")
	}
	for i, media := range api.groupCalls[0] {
		file := media.(tgbotapi.InputMediaPhoto).Media
		if file.(tgbotapi.FileID) != tgbotapi.FileID(ids[i]) {
			t.Fatal("cache ID changed")
		}
	}
}

func TestTikTokPhotoGroupUsesOldTemporaryStatusAndDeletes(t *testing.T) {
	api := &fakeChatAPI{}
	b := &Bot{
		sender: &Sender{api: api, log: zap.NewNop(), sleep: func(time.Duration) {}},
		cfg:    &config.Config{DownloadWidgetsEnabled: true, TikTokPhotosEnabled: true},
		log:    zap.NewNop(),
	}
	st := b.beginPhotoStatus(context.Background(), "group", -555, 123)
	if st.widget != nil || st.textMessage == nil {
		t.Fatal("group must use old text-only progress")
	}
	st.stage("downloading")
	st.stage("sending")
	st.finish(true)
	sent, deleted := api.counts()
	if sent != 1 || deleted != 1 {
		t.Fatalf("old group status should be sent once and deleted: sent=%d delete=%d", sent, deleted)
	}
}

func TestTikTokPhotoPilotGate(t *testing.T) {
	b := &Bot{cfg: &config.Config{TikTokPhotosEnabled: true, TikTokPhotoPilotIDs: map[int64]struct{}{665731091: {}}}}
	if !b.photoActive(665731091) || b.photoActive(777) {
		t.Fatal("pilot leaked")
	}
	b.cfg.TikTokPhotosEnabled = false
	if b.photoActive(665731091) {
		t.Fatal("disabled feature still active")
	}
	b.cfg.TikTokPhotosEnabled = true
	b.cfg.TikTokPhotoPilotIDs = nil
	if !b.photoActive(777) {
		t.Fatal("empty allowlist should be global")
	}
}

func TestTikTokPhotoWidgetDoesNotFabricatePercent(t *testing.T) {
	data, err := widgetStateJSON(downloadWidgetData{Platform: "tiktok", Stage: "downloading", Photo: true, Revision: 10})
	if err != nil || !strings.Contains(data, "\"ph\":true") {
		t.Fatalf("photo widget missing flag: %s %v", data, err)
	}
	if !strings.Contains(downloadWidgetHTML, "Скачиваем фотографии") || !strings.Contains(downloadWidgetHTML, "Фотографии отправлены") {
		t.Fatal("photo stage UI missing")
	}
	pct := 70
	if _, err := widgetStateJSON(downloadWidgetData{Platform: "tiktok", Stage: "downloading", Photo: true, Percent: &pct}); err == nil {
		t.Fatal("fake photo download percentage accepted")
	}
	if _, err := widgetStateJSON(downloadWidgetData{Platform: "instagram", Stage: "downloading", Photo: true}); err == nil {
		t.Fatal("photo flag accepted on unsupported platform")
	}
}
