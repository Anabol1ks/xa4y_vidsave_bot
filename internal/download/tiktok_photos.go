package download

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MaxTikTokPhotos = 35
	maxPhotoBytes   = 10 * 1024 * 1024 // Telegram sendPhoto per-file limit
	maxAlbumBytes   = 80 * 1024 * 1024 // bounded total temp disk allocation
)

var (
	ErrPhotoNotFound  = errors.New("TikTok photos unavailable")
	ErrPhotoTooLarge  = errors.New("TikTok photo exceeds limits")
	ErrPhotoTooMany   = errors.New("TikTok photo carousel exceeds limit")
	ErrPhotoBadFormat = errors.New("TikTok photo has unsupported image format")
	ErrPhotoDownload  = errors.New("TikTok photo extraction failed")
)

// PhotoResult owns its temporary directory. Caller must always Cleanup.
type PhotoResult struct {
	Dir   string
	Files []string
}

func (r *PhotoResult) Cleanup() {
	if r != nil && r.Dir != "" {
		_ = os.RemoveAll(r.Dir)
	}
}

// fileCountAndSize checks actual files on disk rather than relying on
// extractor stderr, which can report success while skipping a photo.
func fileCountAndSize(root string) (int, int64, error) {
	count := 0
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if !d.Type().IsRegular() {
			return ErrPhotoBadFormat
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		count++
		total += info.Size()
		return nil
	})
	return count, total, err
}

func isSafePhotoURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "tiktok.com" && !strings.HasSuffix(host, ".tiktok.com") {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "@") || parts[1] != "photo" {
		return false
	}
	_, err = strconv.ParseUint(parts[2], 10, 64)
	return err == nil
}

// DownloadTikTokPhotos uses only the pinned gallery-dl executable on a
// *canonical* TikTok /photo/ URL. The caller resolves vt/vm short links first.
// No shell, cookies, arbitrary domains or remote-provided output paths.
func DownloadTikTokPhotos(ctx context.Context, rawURL, proxy string) (result *PhotoResult, err error) {
	if !isSafePhotoURL(rawURL) {
		return nil, ErrPhotoBadFormat
	}
	dir, err := os.MkdirTemp("", "vidsave_photos_*")
	if err != nil {
		return nil, fmt.Errorf("%w: cannot create temporary folder", ErrPhotoDownload)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	dlCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	args := []string{
		"--config-ignore", "--no-input", "--no-mtime",
		"--retries", "1", "--http-timeout", "10",
		"--directory", dir, "--filename", "{num:02}.{extension}",
		"--range", "1-36", // extra item detects >35, never silently truncate
		"--option", "extractor.tiktok.photos=true",
		"--option", "extractor.tiktok.videos=false",
		"--option", "extractor.tiktok.audio=false",
		"--option", "extractor.tiktok.covers=false",
	}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	args = append(args, rawURL)
	cmd := exec.CommandContext(dlCtx, "gallery-dl", args...)
	var diagnostics boundedPhotoLog
	// stdout and stderr are copied concurrently by os/exec. Discard
	// progress and filenames, retaining only bounded stderr diagnostics.
	cmd.Stdout = io.Discard
	cmd.Stderr = &diagnostics

	// Cancel maliciously large photo posts while the extractor is running.
	oversize := make(chan struct{}, 1)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n, bytes, scanErr := fileCountAndSize(dir)
				if scanErr == nil && (bytes > maxAlbumBytes || n > MaxTikTokPhotos) {
					select {
					case oversize <- struct{}{}:
					default:
					}
					cancel()
					return
				}
			case <-dlCtx.Done():
				return
			}
		}
	}()
	runErr := cmd.Run()
	cancel()
	<-monitorDone
	select {
	case <-oversize:
		return nil, ErrPhotoTooLarge
	default:
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(dlCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrPhotoDownload
		}
		// Do not expose remote URLs or extractor internals to users.
		_ = diagnostics
		return nil, ErrPhotoDownload
	}

	entries, scanErr := os.ReadDir(dir)
	if scanErr != nil {
		return nil, ErrPhotoDownload
	}
	if len(entries) == 0 {
		return nil, ErrPhotoNotFound
	}
	if len(entries) > MaxTikTokPhotos {
		return nil, ErrPhotoTooMany
	}
	photos := make([]string, 0, len(entries))
	var total int64
	for _, item := range entries {
		if !item.Type().IsRegular() {
			return nil, ErrPhotoBadFormat
		}
		name := item.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
			return nil, ErrPhotoBadFormat
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		order, orderErr := strconv.Atoi(base)
		if orderErr != nil || order < 1 || order > MaxTikTokPhotos {
			return nil, ErrPhotoBadFormat
		}
		info, infoErr := item.Info()
		if infoErr != nil || info.Size() == 0 {
			return nil, ErrPhotoBadFormat
		}
		path := filepath.Join(dir, name)
		if ext == ".webp" {
			converted := filepath.Join(dir, base+".jpg")
			convertCtx, conversionCancel := context.WithTimeout(ctx, 12*time.Second)
			c := exec.CommandContext(convertCtx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-i", path, "-frames:v", "1", "-q:v", "3", converted)
			convErr := c.Run()
			conversionCancel()
			if convErr != nil {
				return nil, ErrPhotoBadFormat
			}
			if removeErr := os.Remove(path); removeErr != nil {
				return nil, ErrPhotoBadFormat
			}
			path = converted
			info, infoErr = os.Stat(path)
			if infoErr != nil {
				return nil, ErrPhotoBadFormat
			}
		}
		if info.Size() > maxPhotoBytes {
			return nil, ErrPhotoTooLarge
		}
		total += info.Size()
		if total > maxAlbumBytes {
			return nil, ErrPhotoTooLarge
		}
		photos = append(photos, path)
	}
	sort.Strings(photos)
	// Detect skipped images, including oversized photos: never present a
	// truncated sequence as a complete album.
	for i, path := range photos {
		num, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		if num != i+1 {
			return nil, ErrPhotoBadFormat
		}
	}
	return &PhotoResult{Dir: dir, Files: photos}, nil
}

// The extractor can print long URLs or error messages; retain only bounded
// diagnostic bytes and never put them in user-facing errors.
type boundedPhotoLog struct{ buf bytes.Buffer }

func (b *boundedPhotoLog) Write(p []byte) (int, error) {
	const max = 4096
	if b.buf.Len() < max {
		_, _ = b.buf.Write(p[:min(len(p), max-b.buf.Len())])
	}
	return len(p), nil
}
