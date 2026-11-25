package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"xa4yy_vidsave/internal/link"
)

const tiktokFallbackMaxBytes int64 = 50 * 1024 * 1024

var errGalleryVideo = errors.New("gallery-dl video extraction failed")

// DownloadTikTokVideoWithFallback attempts a second extractor once, solely
// for an explicit primary webpage extraction error. Never retry access,
// rate, network, size, cancellation, or unknown failures.
func DownloadTikTokVideoWithFallback(ctx context.Context, raw, proxy string, maxBytes int64, log *zap.Logger, progress func(ProgressEvent)) (*VideoResult, error) {
	result, primaryErr := DownloadVideoWithProgress(ctx, raw, proxy, link.TypeTikTok, log, progress)
	if primaryErr == nil || !errors.Is(primaryErr, ErrYtDlpExtraction) || ctx.Err() != nil {
		return result, primaryErr
	}
	// All fallback work remains inside the original caller's deadline.
	fallbackCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	canonical, ok := link.ResolveTikTokVideoURL(fallbackCtx, raw, nil)
	if !ok {
		return nil, primaryErr
	}
	if progress != nil {
		progress(ProgressEvent{Known: false})
	}
	log.Info("trying TikTok video fallback", zap.String("extractor", "gallery-dl"))
	result, err := downloadGalleryVideo(fallbackCtx, canonical, proxy, maxBytes)
	if err == nil {
		log.Info("TikTok video fallback succeeded", zap.String("extractor", "gallery-dl"))
		return result, nil
	}
	log.Warn("TikTok video fallback failed", zap.Error(err))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, ErrYtDlpAuth) || errors.Is(err, ErrYtDlpRestricted) || errors.Is(err, ErrYtDlpPrivate) || errors.Is(err, ErrYtDlpRateLimited) {
		return nil, err
	}
	return nil, primaryErr
}

func galleryVideoError(text string) error {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "region locked"), strings.Contains(lower, "age-restricted"), strings.Contains(lower, "certain audiences"), strings.Contains(lower, "some audiences"), strings.Contains(lower, "ip address is blocked"):
		return ErrYtDlpRestricted
	case strings.Contains(lower, "private account"), strings.Contains(lower, "private video"), strings.Contains(lower, "private post"):
		return ErrYtDlpPrivate
	case strings.Contains(lower, "login required"), strings.Contains(lower, "redirect to login"), strings.Contains(lower, "authentication"), strings.Contains(lower, "authorization"):
		return ErrYtDlpAuth
	case strings.Contains(lower, "http error 429"), strings.Contains(lower, "http 429"), strings.Contains(lower, "too many requests"):
		return ErrYtDlpRateLimited
	}
	return errGalleryVideo
}

func downloadGalleryVideo(ctx context.Context, canonical, proxy string, maxBytes int64) (*VideoResult, error) {
	if maxBytes <= 0 || maxBytes > tiktokFallbackMaxBytes {
		maxBytes = tiktokFallbackMaxBytes
	}
	dir, err := os.MkdirTemp("", "vidsave_tiktok_video_*")
	if err != nil {
		return nil, errGalleryVideo
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{
		"--config-ignore", "--no-input", "--no-mtime", "--retries", "0", "--http-timeout", "10",
		"--directory", dir, "--filename", "video.{extension}", "--range", "1-2",
		"--filesize-max", strconv.FormatInt(maxBytes, 10),
		"-o", "extractor.tiktok.photos=false", "-o", "extractor.tiktok.audio=false",
		"-o", "extractor.tiktok.videos=true", "-o", "extractor.tiktok.covers=false",
		"-o", "extractor.tiktok.subtitles=false",
	}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	args = append(args, canonical)
	cmd := exec.CommandContext(dlCtx, "gallery-dl", args...)
	var diagnostics boundedPhotoLog
	cmd.Stdout = io.Discard
	cmd.Stderr = &diagnostics
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-dlCtx.Done():
				return
			case <-ticker.C:
				count, size, scanErr := fileCountAndSize(dir)
				if scanErr != nil || count > 2 || size > maxBytes {
					cancel()
					return
				}
			}
		}
	}()
	runErr := cmd.Run()
	canceled := dlCtx.Err()
	cancel()
	<-done
	// gallery-dl can return exit code zero for an unavailable post.
	if class := galleryVideoError(diagnostics.buf.String()); class != errGalleryVideo {
		return nil, class
	}
	if runErr != nil || canceled != nil || ctx.Err() != nil {
		return nil, errGalleryVideo
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "video.mp4" || !entries[0].Type().IsRegular() {
		return nil, errGalleryVideo
	}
	path := filepath.Join(dir, "video.mp4")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes {
		return nil, errGalleryVideo
	}
	// Verify both container signature and video stream; never send a photo,
	// partial file, HTML error page or non-video as a Telegram MP4.
	f, err := os.Open(path)
	if err != nil {
		return nil, errGalleryVideo
	}
	header := make([]byte, 12)
	_, err = io.ReadFull(f, header)
	_ = f.Close()
	if err != nil || string(header[4:8]) != "ftyp" {
		return nil, errGalleryVideo
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer probeCancel()
	probe := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-select_streams", "V:0", "-show_entries", "stream=codec_type", "-of", "default=noprint_wrappers=1:nokey=1", path)
	output, err := probe.Output()
	if err != nil || strings.TrimSpace(string(output)) != "video" {
		return nil, fmt.Errorf("%w: invalid MP4 video stream", errGalleryVideo)
	}
	keep = true
	return &VideoResult{FilePath: path}, nil
}
