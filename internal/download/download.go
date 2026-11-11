package download

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"xa4yy_vidsave/internal/link"

	"go.uber.org/zap"
)

var (
	ErrVideoNotFound    = errors.New("video not found")
	ErrYtDlp            = errors.New("yt-dlp error")
	ErrYtDlpAuth        = errors.New("yt-dlp authentication required")
	ErrYtDlpUnsupported = errors.New("yt-dlp unsupported url")
	ErrTikTokPhotoPost  = errors.New("tiktok photo post is not a video")
	ErrYtDlpPrivate     = errors.New("yt-dlp private video")
	ErrYtDlpRateLimited = errors.New("yt-dlp source rate limited")
	ErrYtDlpNetwork     = errors.New("yt-dlp network failure")
	ErrVideoTooLong     = errors.New("youtube video exceeds shorts duration limit")
)

// VideoResult содержит путь к скачанному файлу.
type VideoResult struct {
	FilePath string
}

// DownloadVideo скачивает видео по оригинальному URL через yt-dlp.
// Работает с Instagram Reels, TikTok и YouTube Shorts.
// Возвращает путь к временному файлу (без водяного знака).
// proxy — строка вида "socks5h://host:port" или "http://host:port" (может быть пустой).
func DownloadVideo(ctx context.Context, rawURL string, proxy string, platform link.Type, log *zap.Logger) (*VideoResult, error) {
	return DownloadVideoWithProgress(ctx, rawURL, proxy, platform, log, nil)
}

// DownloadVideoWithProgress preserves the original video/download workflow,
// reporting only measured bytes of the current yt-dlp stream.
func DownloadVideoWithProgress(ctx context.Context, rawURL, proxy string, platform link.Type, log *zap.Logger, onProgress func(ProgressEvent)) (*VideoResult, error) {
	// Создаём временную директорию для скачивания
	tmpDir, err := os.MkdirTemp("", "vidsave_*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	outTemplate := filepath.Join(tmpDir, "video.%(ext)s")

	args := ytDlpArgs(rawURL, outTemplate, proxy, platform)
	if onProgress != nil {
		args = append(args[:len(args)-1], append([]string{
			"--progress", "--newline", "--no-color", "--progress-template", progressTemplate,
		}, args[len(args)-1])...)
	}

	log.Debug("running yt-dlp", zap.Strings("args", args))

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)

	var stdout, stderr bytes.Buffer
	var stdoutCapture, stderrCapture *progressWriter
	if onProgress != nil {
		// --print after_move:filepath implies --quiet; yt-dlp can send its
		// --progress-template to stdout, not stderr. Parse BOTH streams,
		// while retaining the non-progress stdout line containing filepath.
		// os/exec copies stdout and stderr in separate goroutines; callbacks
		// must be serialized, even if both streams emit progress.
		var progressMu sync.Mutex
		callback := func(event ProgressEvent) {
			progressMu.Lock()
			defer progressMu.Unlock()
			onProgress(event)
		}
		stdoutCapture = &progressWriter{callback: callback}
		stderrCapture = &progressWriter{callback: callback}
		cmd.Stdout = stdoutCapture
		cmd.Stderr = stderrCapture
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	cmdErr := cmd.Run()
	stdoutText, stderrText := stdout.String(), stderr.String()
	if stdoutCapture != nil {
		stdoutText = stdoutCapture.diagnostic()
		stderrText = stderrCapture.diagnostic()
	}
	if cmdErr != nil {
		downloadErr := classifyYtDlpError(stderrText)
		log.Error("yt-dlp failed",
			zap.Error(cmdErr),
			zap.String("classified_error", downloadErr.Error()),
			zap.String("stderr", stderrText),
			zap.String("stdout", stdoutText),
		)
		os.RemoveAll(tmpDir)
		if err := ctx.Err(); err != nil { return nil, err }
		return nil, fmt.Errorf("%w: %s", downloadErr, stderrText)
	}

	// --print after_move:filepath выводит путь к итоговому файлу в stdout
	filePath := finalFilePath(stdoutText, tmpDir)
	log.Debug("yt-dlp output path", zap.String("raw_stdout", filePath))

	// Если путь пустой или файла нет — ищем любой файл в tmp-директории
	if filePath == "" || !fileExists(filePath) {
		log.Debug("printed path not found, scanning tmpDir", zap.String("tmpDir", tmpDir))
		filePath = findFirstFile(tmpDir)
	}

	if filePath == "" {
		if strings.Contains(strings.ToLower(stdoutText+" "+stderrText), "does not pass filter") && platform == link.TypeYouTube {
			os.RemoveAll(tmpDir)
			return nil, ErrVideoTooLong
		}
		// Логируем содержимое tmpDir для отладки
		entries, _ := os.ReadDir(tmpDir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		log.Error("no files found in tmpDir", zap.Strings("files", names))
		os.RemoveAll(tmpDir)
		return nil, ErrVideoNotFound
	}

	log.Info("video downloaded", zap.String("path", filePath))
	return &VideoResult{FilePath: filePath}, nil
}

// ytDlpArgs keeps existing TikTok/Instagram behavior and selects an MP4
// video+audio pair for YouTube. Vertical Shorts can be up to 1080x1920;
// limit by width, not height, to preserve portrait quality.
func ytDlpArgs(rawURL, outTemplate, proxy string, platform link.Type) []string {
	args := []string{
		"--no-warnings",
		"--no-playlist",
		"--no-overwrites",
		"-o", outTemplate,
		"--max-filesize", "50M",
		"--socket-timeout", "30",
		"--retries", "3",
		"--print", "after_move:filepath",
	}
	if platform == link.TypeYouTube {
		args = append(args,
			"-f", "bv[ext=mp4][vcodec^=avc1][width<=1080]+ba[ext=m4a]/b[ext=mp4][vcodec^=avc1]/bv[ext=mp4][width<=1080]+ba[ext=m4a]/b[ext=mp4]",
			"--merge-output-format", "mp4",
			"--js-runtimes", "node",
			"--match-filter", "duration <= 180 & !is_live",
		)
	} else {
		args = append(args, "-f", "best")
	}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	return append(args, rawURL)
}

// finalFilePath selects the last existing yt-dlp after_move filepath line.

// In progress mode stdout also includes our progress-template events, so
// treating all of stdout as a single path would fail or select an interim file.
func finalFilePath(stdout, tmpDir string) string {
	lines := strings.Split(stdout, "\n")
	base, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		return ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(lines[i])
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		path, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if fileExists(path) {
			return path
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func findFirstFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func classifyYtDlpError(stderr string) error {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "private video"), strings.Contains(lower, "private account"), strings.Contains(lower, "this account is private"):
		return ErrYtDlpPrivate
	case strings.Contains(lower, "login required"), strings.Contains(lower, "cookies"), strings.Contains(lower, "authentication"), strings.Contains(lower, "sign in to confirm"), strings.Contains(lower, "confirm you're not a bot"):
		return ErrYtDlpAuth
	case strings.Contains(lower, "http error 429"), strings.Contains(lower, "too many requests"), strings.Contains(lower, "rate limit reached"), strings.Contains(lower, "rate-limit reached"), strings.Contains(lower, "rate limited"):
		return ErrYtDlpRateLimited
	case strings.Contains(lower, "unsupported url") &&
		strings.Contains(lower, "tiktok.com/") && strings.Contains(lower, "/photo/"):
		// Short links (vt.tiktok.com/...) resolve inside yt-dlp to /photo/.
		// They are legitimate photo posts, but not downloadable videos.
		return ErrTikTokPhotoPost
	case strings.Contains(lower, "unsupported url"):
		return ErrYtDlpUnsupported
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "connection reset"), strings.Contains(lower, "connection refused"), strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "temporary failure in name resolution"):
		return ErrYtDlpNetwork
	default:
		return ErrYtDlp
	}
}
