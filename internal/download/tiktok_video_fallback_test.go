package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

const fallbackTestURL = "https://www.tiktok.com/@u/video/123"
const extractionFailure = "echo 'ERROR: Unexpected response from webpage request' >&2; exit 1\n"

func installGalleryFixture(t *testing.T, dir, behavior string) {
	t.Helper()
	script := `#!/bin/sh
echo called >> "` + filepath.Join(dir, "gallery-called") + `"
out=""
while [ "$#" -gt 0 ]; do
 if [ "$1" = "--directory" ]; then shift; out="$1"; fi
 shift
done
` + behavior
	if err := os.WriteFile(filepath.Join(dir, "gallery-dl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\nprintf 'video\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

const fixtureMP4 = `printf '\000\000\000\040ftypisom' > "$out/video.mp4"
`

func TestTikTokVideoFallbackSuccessAndCleanup(t *testing.T) {
	dir := fakeDownloader(t, extractionFailure)
	installGalleryFixture(t, dir, fixtureMP4)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var reset bool
	got, err := DownloadTikTokVideoWithFallback(context.Background(), fallbackTestURL, "", 100, zap.NewNop(), func(p ProgressEvent) {
		if !p.Known {
			reset = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(got.FilePath))
	if !reset {
		t.Fatal("fallback progress not reset")
	}
	data, err := os.ReadFile(filepath.Join(dir, "gallery-called"))
	if err != nil || string(data) != "called\n" {
		t.Fatalf("fallback calls: %q %v", data, err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 1 {
		t.Fatalf("primary tmp leaked: %v %v", entries, err)
	}
}

func TestTikTokFallbackDoesNotRetryNonTechnicalErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    error
	}{
		{"This post may not be comfortable for some audiences. Log in for access. Use --cookies.", ErrYtDlpRestricted},
		{"Your IP address is blocked from accessing this post", ErrYtDlpRestricted},
		{"HTTP Error 403: Forbidden; Unexpected response from webpage request", ErrYtDlp},
		{"Log in for access", ErrYtDlpAuth},
		{"login required", ErrYtDlpAuth},
		{"private video", ErrYtDlpPrivate},
		{"HTTP Error 429: Too Many Requests", ErrYtDlpRateLimited},
		{"connection reset", ErrYtDlpNetwork},
		{"Unsupported URL", ErrYtDlpUnsupported},
		{"other failure", ErrYtDlp},
	} {
		t.Run(tc.message, func(t *testing.T) {
			dir := fakeDownloader(t, fmt.Sprintf("echo '%s' >&2; exit 1\n", tc.message))
			installGalleryFixture(t, dir, fixtureMP4)
			_, err := DownloadTikTokVideoWithFallback(context.Background(), fallbackTestURL, "", 100, zap.NewNop(), nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "gallery-called")); !os.IsNotExist(err) {
				t.Fatal("fallback must not run")
			}
		})
	}
}

func TestTikTokFallbackFailureAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		limit        int64
		want         error
	}{
		{"empty", "exit 0\n", 100, ErrYtDlpExtraction},
		{"oversize", fixtureMP4, 5, ErrYtDlpExtraction},
		{"invalid", `printf 'html' > "$out/video.mp4"`, 100, ErrYtDlpExtraction},
		{"photo", `printf 'photo' > "$out/video.jpg"`, 100, ErrYtDlpExtraction},
		{"extra", fixtureMP4 + `printf 'other' > "$out/other.mp4"`, 100, ErrYtDlpExtraction},
		{"symlink", `ln -s /etc/hosts "$out/video.mp4"`, 100, ErrYtDlpExtraction},
		{"restricted", `echo 'Region locked' >&2; exit 0`, 100, ErrYtDlpRestricted},
		{"login", `echo 'Login required to access this post' >&2; exit 0`, 100, ErrYtDlpAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeDownloader(t, extractionFailure)
			installGalleryFixture(t, dir, tc.script+"\n")
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			got, err := DownloadTikTokVideoWithFallback(context.Background(), fallbackTestURL, "", tc.limit, zap.NewNop(), nil)
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got %v %v want %v", got, err, tc.want)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary files leaked: %v %v", entries, err)
			}
		})
	}
}

func TestTikTokFallbackPrimarySuccessAndCancellation(t *testing.T) {
	t.Run("primary success", func(t *testing.T) {
		dir := fakeDownloader(t, `
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-o" ]; then shift; out="$1"; fi
 shift
done
file=$(printf '%s' "$out" | sed 's/%(ext)s/mp4/g')
printf 'primary' > "$file"
printf '%s\n' "$file"
`)
		installGalleryFixture(t, dir, fixtureMP4)
		got, err := DownloadTikTokVideoWithFallback(context.Background(), fallbackTestURL, "", 100, zap.NewNop(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(filepath.Dir(got.FilePath))
		if _, err := os.Stat(filepath.Join(dir, "gallery-called")); !os.IsNotExist(err) {
			t.Fatal("unnecessary fallback")
		}
	})
	t.Run("shared deadline", func(t *testing.T) {
		dir := fakeDownloader(t, extractionFailure)
		installGalleryFixture(t, dir, "exec sleep 10\n")
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := DownloadTikTokVideoWithFallback(ctx, fallbackTestURL, "", 100, zap.NewNop(), nil)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
			t.Fatalf("deadline not enforced: %v", err)
		}
		entries, _ := os.ReadDir(tmp)
		if len(entries) != 0 {
			t.Fatal("temp leak")
		}
	})
}
