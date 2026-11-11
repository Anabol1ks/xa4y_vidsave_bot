package bot

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/link"

	"go.uber.org/zap"
)

func TestErrorWidgetHtmlAndOfflineButton(t *testing.T) {
	raw := []byte(errorWidgetHTML)
	if len(raw) == 0 || len(raw) > 32768 {
		t.Fatalf("HTML length %d", len(raw))
	}
	var buffer bytes.Buffer
	compressed, _ := zlib.NewWriterLevel(&buffer, 9)
	if _, err := compressed.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() > 4096 {
		t.Fatalf("compressed HTML length %d", buffer.Len())
	}
	for _, s := range []string{"Поддерживаемые форматы", "Instagram Reels", "TikTok", "YouTube Shorts", "aria-expanded", "addEventListener('click'", "chatDataChanged", "photo_post", "Это фотографии TikTok", "source_rate_limited", "auth_required", "too_large", "send_error", "busy", "download_error", "invalid_url"} {
		if !strings.Contains(errorWidgetHTML, s) {
			t.Fatalf("missing static content: %s", s)
		}
	}
	for _, s := range []string{"TelegramWidget.request(", "fetch(", "XMLHttpRequest", "WebSocket", "Попробовать снова", "retryDownload", "window.location", "openLink(", "start-download"} {
		if strings.Contains(errorWidgetHTML, s) {
			t.Fatalf("unsafe feature in error widget: %s", s)
		}
	}
}

func TestErrorWidgetPayloadAllowlist(t *testing.T) {
	for _, p := range []string{"youtube", "tiktok", "instagram", "generic"} {
		for code := range allowedErrorWidgetCodes {
			data, err := marshalErrorWidgetState(p, code)
			if err != nil {
				t.Fatalf("%s / %s %v", p, code, err)
			}
			var d errorWidgetState
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				t.Fatal(err)
			}
			if d.Platform != p || d.Code != code || d.Version != 1 || len(data) > 1024 {
				t.Fatal("invalid payload", data)
			}
			if strings.Contains(data, "telegram") || strings.Contains(data, "http") {
				t.Fatal("user data or URL leaked")
			}
		}
	}
	for _, test := range []struct{ p, code string }{
		{"evil", "download_error"}, {"tiktok", "retry"}, {"tiktok", "http://example.com"},
		{"tiktok", "redownload"}, {"tiktok", ""}, {"", "auth_required"},
	} {
		if _, err := marshalErrorWidgetState(test.p, test.code); err == nil {
			t.Fatalf("accepted invalid error state: %+v", test)
		}
	}
}

func TestErrorWidgetSendAllPlatformsNoQuery(t *testing.T) {
	fake := &widgetServer{}
	srv := fake.endpoint(t)
	defer srv.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(srv, chat, true)
	b.cfg.ErrorWidgetsEnabled = true
	platforms := []string{"instagram", "tiktok", "youtube", "generic"}
	for _, p := range platforms {
		if !b.sendErrorWidget(123, 66, p, "unsupported_url") {
			t.Fatalf("failed sending %s", p)
		}
	}
	calls := fake.callsFor("sendWidget")
	if len(calls) != len(platforms) {
		t.Fatalf("got %d calls", len(calls))
	}
	for i, c := range calls {
		if c.params.Get("allow_queries") != "" || c.params.Get("public_data") != "" || c.params.Get("is_shared") != "" {
			t.Fatal("unexpected active/shared widget capabilities")
		}
		if !strings.Contains(c.params.Get("reply_parameters"), "66") {
			t.Fatal("reply lost")
		}
		var state errorWidgetState
		if err := json.Unmarshal([]byte(c.params.Get("chat_data")), &state); err != nil {
			t.Fatal(err)
		}
		if state.Platform != platforms[i] || state.Code != "unsupported_url" {
			t.Fatal("incorrect state", state)
		}
	}
	sent, deleted := chat.counts()
	if sent != 0 || deleted != 0 {
		t.Fatal("spurious text/delete activity")
	}
}

func TestErrorWidgetDisabledAndFallback(t *testing.T) {
	fake := &widgetServer{fail: true}
	srv := fake.endpoint(t)
	defer srv.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(srv, chat, true)
	b.cfg.ErrorWidgetsEnabled = true
	b.errorWidgetOrText(123, 0, "youtube", "send_error", "fallback message", true)
	sent, _ := chat.counts()
	if sent != 1 {
		t.Fatal("error fallback text missing")
	}
	if len(fake.callsFor("sendWidget")) != 1 {
		t.Fatal("error widget not attempted")
	}
	// Groups and non-pilot users get text only, never sendWidget.
	b.errorWidgetOrText(-123, 0, "youtube", "send_error", "group fallback", false)
	if len(fake.callsFor("sendWidget")) != 1 {
		t.Fatal("widget attempted in group")
	}
	b.cfg.DownloadWidgetPilotIDs = map[int64]struct{}{665731091: {}}
	b.errorWidgetOrText(123, 0, "tiktok", "auth_required", "not in pilot", true)
	if len(fake.callsFor("sendWidget")) != 1 {
		t.Fatal("nonpilot received widget")
	}
	sent, _ = chat.counts()
	if sent != 3 {
		t.Fatalf("missing text fallbacks: %d", sent)
	}
}

func TestProgressFailureProducesOnlyOneErrorCard(t *testing.T) {
	fake := &widgetServer{}
	srv := fake.endpoint(t)
	defer srv.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(srv, chat, true)
	b.cfg.ErrorWidgetsEnabled = true
	st := b.beginDownloadStatus(context.Background(), "private", 123, 77, link.TypeInstagram)
	if st.widget == nil {
		t.Fatal("live status did not start")
	}
	st.stage("downloading")
	st.showError("auth_required", "auth required fallback")
	st.finish(false)
	// Wait for asynchronous delete of the old progress widget.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, del := chat.counts()
		if del == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := fake.callsFor("sendWidget")
	if len(calls) != 2 {
		t.Fatalf("wanted progress + error widget: got %d", len(calls))
	}
	var errState errorWidgetState
	if err := json.Unmarshal([]byte(calls[1].params.Get("chat_data")), &errState); err != nil {
		t.Fatal(err)
	}
	if errState.Code != "auth_required" || errState.Platform != "instagram" {
		t.Fatal(errState)
	}
	texts, deletes := chat.counts()
	if texts != 0 || deletes != 1 {
		t.Fatalf("duplicate text or wrong delete, text=%d deletes=%d", texts, deletes)
	}
}

func TestProgressErrorWidgetFailureFallsBackOnce(t *testing.T) {
	mu := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendWidget") {
			if mu {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("error"))
				return
			}
			mu = true
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":43}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()
	chat := &fakeChatAPI{}
	b := &Bot{cfg: &config.Config{DownloadWidgetsEnabled: true, ErrorWidgetsEnabled: true},
		sender: &Sender{api: chat, log: zap.NewNop(), sleep: func(time.Duration) {}},
		log:    zap.NewNop(), downloadWidgetAPI: &downloadWidgetAPI{endpoint: srv.URL, client: &http.Client{Timeout: time.Second}},
	}
	st := b.beginDownloadStatus(context.Background(), "private", 123, 0, link.TypeTikTok)
	if st.widget == nil {
		t.Fatal("progress status failed")
	}
	st.showError("download_error", "failed downloading (fallback)")
	st.finish(false)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, deletes := chat.counts()
		if deletes > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, deletes := chat.counts()
	if texts != 1 || deletes != 1 {
		t.Fatalf("missing text fallback or duplicated message %d %d", texts, deletes)
	}
}

func TestParseErrorsShowWidgetOnlyForLinks(t *testing.T) {
	fake := &widgetServer{}
	srv := fake.endpoint(t)
	defer srv.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(srv, chat, true)
	b.cfg.ErrorWidgetsEnabled = true
	b.downloadGate = newDownloadGate(nil)
	b.handleParseError(123, "https://example.com/shorts/1", link.ErrNotAllowedHost)
	if len(fake.callsFor("sendWidget")) != 1 {
		t.Fatal("unsupported URL did not produce widget")
	}
	// Repeated invalid URLs are rate-limited to avoid widget spam.
	b.handleParseError(123, "https://example.com/shorts/2", link.ErrNotAllowedHost)
	if len(fake.callsFor("sendWidget")) != 1 {
		t.Fatal("parse error widget spam")
	}
	// Ordinary chat text still gets the existing plain-text response.
	b.handleParseError(321, "hello", link.ErrNotURL)
	sent, _ := chat.counts()
	if sent != 1 {
		t.Fatal("normal text should not create widget")
	}
}
