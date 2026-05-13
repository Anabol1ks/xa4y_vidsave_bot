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

	"go.uber.org/zap"
)

var (
	ErrVideoNotFound    = errors.New("video not found")
	ErrYtDlp            = errors.New("yt-dlp error")
	ErrYtDlpAuth        = errors.New("yt-dlp authentication required")
	ErrYtDlpUnsupported = errors.New("yt-dlp unsupported url")
)

// VideoResult содержит путь к скачанному файлу.
type VideoResult struct {
	FilePath string
}

// DownloadVideo скачивает видео по оригинальному URL через yt-dlp.
// Работает с Instagram Reels, TikTok и другими поддерживаемыми сайтами.
// Возвращает путь к временному файлу (без водяного знака).
// proxy — строка вида "socks5h://host:port" или "http://host:port" (может быть пустой).
func DownloadVideo(ctx context.Context, rawURL string, proxy string, log *zap.Logger) (*VideoResult, error) {
	// Создаём временную директорию для скачивания
	tmpDir, err := os.MkdirTemp("", "vidsave_*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	outTemplate := filepath.Join(tmpDir, "video.%(ext)s")

	args := []string{
		"--no-warnings",
		"--no-playlist",
		"--no-overwrites",
		"-f", "best",
		"-o", outTemplate,
		// Лимит размера файла — 50 MB (Telegram Bot API limit)
		"--max-filesize", "50M",
		// Таймаут на сокет-операции (не зависать вечно)
		"--socket-timeout", "30",
		// Количество ретраев при ошибках сети
		"--retries", "3",
		// Выводим итоговый путь к файлу после всех перемещений/мержей
		"--print", "after_move:filepath",
	}

	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}

	args = append(args, rawURL)

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
	case strings.Contains(lower, "login required"), strings.Contains(lower, "cookies"), strings.Contains(lower, "authentication"):
		return ErrYtDlpAuth
	case strings.Contains(lower, "unsupported url"):
		return ErrYtDlpUnsupported
	default:
		return ErrYtDlp
	}
}
