package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"xa4yy_vidsave/internal/link"
)

func TestProgressParser(t *testing.T) {
	cases := []struct {
		line         string
		valid, known bool
		pct          int
	}{
		{progressPrefix + "downloading|680|1000|NA", true, true, 68},
		{progressPrefix + "downloading|1000|1000|NA", true, true, 100},
		{progressPrefix + "downloading|1500|1000|NA", true, true, 100},
		{progressPrefix + "downloading|0|1000|NA", true, true, 0},
		{progressPrefix + "finished|100|100|NA", true, true, 100},
		{progressPrefix + "downloading|512|NA|1000", true, false, 0},
		{progressPrefix + "downloading|512|0|1000", true, false, 0},
		{progressPrefix + "downloading|bad|1000|1000", true, false, 0},
		{"ERROR: unauthorized", false, false, 0},
		{progressPrefix + "unknown|512|1024|NA", false, false, 0},
		{progressPrefix + "downloading|512|1024", false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			event, ok := parseProgressLine(tc.line)
			if ok != tc.valid || event.Known != tc.known || event.Percent != tc.pct {
				t.Fatalf("progress %+v ok=%t want known=%t pct=%d", event, ok, tc.known, tc.pct)
			}
		})
	}
}

func TestProgressWriterCRLFAndBoundedDiagnostic(t *testing.T) {
	var events []ProgressEvent
	writer := &progressWriter{callback: func(p ProgressEvent) { events = append(events, p) }}
	input := []byte("WARN: ready\r" + progressPrefix + "downloading|5|10|NA\r\n" + progressPrefix + "downloading|9|10|NA\n")
	if n, err := writer.Write(input); err != nil || n != len(input) {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Percent != 50 || events[1].Percent != 90 {
		t.Fatalf("events=%+v", events)
	}
	if !strings.Contains(writer.diagnostic(), "WARN: ready") {
		t.Fatal("warning missing")
	}
	_, _ = writer.Write([]byte(strings.Repeat("x", 9000) + "\n"))
	if len(writer.diagnostic()) > 65536 {
		t.Fatal("stderr logging must be bounded")
	}
}

func fakeDownloader(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestDownloadVideoWithProgressFakeExtractor(t *testing.T) {
	fakeDownloader(t, `
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift
done
file=$(printf '%s' "$out" | sed 's/%(ext)s/mp4/g')
printf 'abcdefg' > "$file"
printf 'VIDSAVE_PROGRESS|downloading|20|100|NA\n'
printf 'VIDSAVE_PROGRESS|downloading|90|100|NA\n' >&2
printf '%s\n' "$file"
`)
	var actual []ProgressEvent
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := DownloadVideoWithProgress(ctx, "https://www.tiktok.com/@u/video/1", "", link.TypeTikTok, zap.NewNop(), func(p ProgressEvent) { actual = append(actual, p) })
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(res.FilePath))
	if len(actual) != 2 {
		t.Fatalf("stdout/stderr progress missing: %+v", actual)
	}
	found := map[int]bool{}
	for _, event := range actual {
		found[event.Percent] = true
	}
	if !found[20] || !found[90] {
		t.Fatalf("progress from one stream dropped: %+v", actual)
	}
	if _, err := os.Stat(res.FilePath); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadVideoWithProgressAuthError(t *testing.T) {
	fakeDownloader(t, "echo 'ERROR: login required' >&2\nexit 1\n")
	_, err := DownloadVideoWithProgress(context.Background(), "https://www.instagram.com/reel/1", "", link.TypeInstagram, zap.NewNop(), func(ProgressEvent) {})
	if !errors.Is(err, ErrYtDlpAuth) {
		t.Fatalf("classification: %v", err)
	}
}

func TestDownloadVideoWithProgressCancellation(t *testing.T) {
	fakeDownloader(t, "exec sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := DownloadVideoWithProgress(ctx, "https://www.youtube.com/shorts/1", "", link.TypeYouTube, zap.NewNop(), func(ProgressEvent) {})
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("cancellation ineffective: %v %v", err, time.Since(started))
	}
}

func TestFinalFilePathRejectsExternalPaths(t *testing.T) {
	inside := t.TempDir()
	outside := t.TempDir()
	final := filepath.Join(inside, "video.mp4")
	external := filepath.Join(outside, "secret.mp4")
	if err := os.WriteFile(final, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	value := "VIDSAVE_PROGRESS|downloading|5|10|NA\n" + external + "\n" + final + "\n"
	if got := finalFilePath(value, inside); got != final {
		t.Fatalf("expected %q, got %q", final, got)
	}
	if got := finalFilePath(external, inside); got != "" {
		t.Fatalf("external path accepted: %s", got)
	}
}
