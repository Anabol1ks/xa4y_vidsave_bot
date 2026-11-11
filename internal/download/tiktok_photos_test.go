package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mockPhotoGallery(t *testing.T, shell string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gallery-dl"), []byte("#!/bin/sh\n"+shell), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const demoPhotoURL = "https://www.tiktok.com/@isisusisp/photo/7706556456627096852"

func TestTikTokPhotosExtractorMockThreeImages(t *testing.T) {
	mockPhotoGallery(t, `
dir=""
while [ "$#" -gt 0 ]; do
 case "$1" in
 --directory) shift; dir="$1";;
 esac
 shift
done
printf 'image1' > "$dir/01.jpg"
printf 'image2' > "$dir/02.jpg"
printf 'image3' > "$dir/03.png"
`)
	res, err := DownloadTikTokPhotos(context.Background(), demoPhotoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	if len(res.Files) != 3 {
		t.Fatalf("want 3 files, got %+v", res.Files)
	}
	for i, p := range res.Files {
		if !strings.HasPrefix(filepath.Base(p), "0"+strconv.Itoa(i+1)) {
			t.Fatal("wrong ordering:", res.Files)
		}
	}
	res.Cleanup()
	if _, err := os.Stat(res.Dir); !os.IsNotExist(err) {
		t.Fatal("temp files not cleaned")
	}
}

func TestTikTokPhotosRejectInvalidInputBeforeProcess(t *testing.T) {
	for _, raw := range []string{
		"http://www.tiktok.com/@u/photo/123",
		"https://www.tiktok.com/@u/video/123",
		"https://tiktok.com.evil.test/@u/photo/123",
		"https://127.0.0.1:8000/@u/photo/123",
		"https://www.tiktok.com/@u/photo/notanumber",
	} {
		if _, err := DownloadTikTokPhotos(context.Background(), raw, ""); !errors.Is(err, ErrPhotoBadFormat) {
			t.Fatalf("%q: expected BadFormat, got %v", raw, err)
		}
	}
}

func TestTikTokPhotosLimitAndNoPartialAlbum(t *testing.T) {
	t.Run("too many", func(t *testing.T) {
		mockPhotoGallery(t, `
dir=""
while [ "$#" -gt 0 ]; do if [ "$1" = "--directory" ]; then shift;dir="$1"; fi;shift; done
i=1
while [ "$i" -le 36 ]; do
 n=$(printf '%02d' "$i")
 printf 'image' > "$dir/$n.jpg"
 i=$((i+1))
done
`)
		_, err := DownloadTikTokPhotos(context.Background(), demoPhotoURL, "")
		if !errors.Is(err, ErrPhotoTooMany) {
			t.Fatalf("want TooMany, got %v", err)
		}
	})
	t.Run("gap from skipped photo", func(t *testing.T) {
		mockPhotoGallery(t, `
dir=""
while [ "$#" -gt 0 ]; do if [ "$1" = "--directory" ]; then shift;dir="$1"; fi;shift; done
printf 'image' > "$dir/01.jpg"
printf 'image' > "$dir/03.jpg"
`)
		_, err := DownloadTikTokPhotos(context.Background(), demoPhotoURL, "")
		if !errors.Is(err, ErrPhotoBadFormat) {
			t.Fatalf("want BadFormat, got %v", err)
		}
	})
	t.Run("large file", func(t *testing.T) {
		mockPhotoGallery(t, `
dir=""
while [ "$#" -gt 0 ]; do if [ "$1" = "--directory" ]; then shift;dir="$1"; fi;shift; done
head -c 11000000 /dev/zero > "$dir/01.jpg"
`)
		_, err := DownloadTikTokPhotos(context.Background(), demoPhotoURL, "")
		if !errors.Is(err, ErrPhotoTooLarge) {
			t.Fatalf("want TooLarge, got %v", err)
		}
	})
}

func TestTikTokPhotosExecutionCancellation(t *testing.T) {
	mockPhotoGallery(t, "exec sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := DownloadTikTokPhotos(ctx, demoPhotoURL, "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("download not cancelled promptly: %v", err)
	}
}
