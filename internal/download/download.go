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

	"xa4yy_vidsave/internal/link"

	"go.uber.org/zap"
)

var (
	ErrVideoNotFound    = errors.New("video not found")
	ErrYtDlp            = errors.New("yt-dlp error")
	ErrYtDlpAuth        = errors.New("yt-dlp authentication required")
	ErrYtDlpUnsupported = errors.New("yt-dlp unsupported url")
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
	// Создаём временную директорию для скачивания
	tmpDir, err := os.MkdirTemp("", "vidsave_*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	outTemplate := filepath.Join(tmpDir, "video.%(ext)s")

	args := ytDlpArgs(rawURL, outTemplate, proxy, platform)

	log.Debug("running yt-dlp", zap.Strings("args", args))

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		downloadErr := classifyYtDlpError(stderr.String())
		log.Error("yt-dlp failed",
			zap.Error(err),
			zap.String("classified_error", downloadErr.Error()),
			zap.String("stderr", stderr.String()),
			zap.String("stdout", stdout.String()),
		)
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("%w: %s", downloadErr, stderr.String())
	}

	// --print after_move:filepath выводит путь к итоговому файлу в stdout
	filePath := strings.TrimSpace(stdout.String())
	log.Debug("yt-dlp output path", zap.String("raw_stdout", filePath))

	// Если путь пустой или файла нет — ищем любой файл в tmp-директории
	if filePath == "" || !fileExists(filePath) {
		log.Debug("printed path not found, scanning tmpDir", zap.String("tmpDir", tmpDir))
		filePath = findFirstFile(tmpDir)
	}

	if filePath == "" {
		if strings.Contains(strings.ToLower(stdout.String()+" "+stderr.String()), "does not pass filter") && platform == link.TypeYouTube {
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
			"-f", "bv[ext=mp4][width<=1080]+ba[ext=m4a]/b[ext=mp4]",
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
	case strings.Contains(lower, "login required"), strings.Contains(lower, "cookies"), strings.Contains(lower, "authentication"), strings.Contains(lower, "sign in to confirm"), strings.Contains(lower, "confirm you're not a bot"):
		return ErrYtDlpAuth
	case strings.Contains(lower, "unsupported url"):
		return ErrYtDlpUnsupported
	default:
		return ErrYtDlp
	}
}
