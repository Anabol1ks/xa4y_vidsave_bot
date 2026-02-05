package config

import (
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

type Config struct {
	BotToken           string
	AllowedHosts       map[string]struct{}
	InsecureSkipVerify bool
	MaxDownloadBytes   int64
	Proxy              string
}

func getEnv(key string, log *zap.Logger) string {
	if val, exists := os.LookupEnv(key); exists {
		return val
	}
	log.Error("Обязательная переменная окружения не установлена", zap.String("key", key))
	panic("missing required environment variable: " + key)
}

func Load(log *zap.Logger) *Config {
	return &Config{
		BotToken:           strings.TrimSpace(getEnv("BOT_TOKEN", log)),
		AllowedHosts:       parseAllowedHosts(getEnv("ALLOWED_HOSTS", log)),
		InsecureSkipVerify: parseBool(getEnv("INSECURE_SKIP_VERIFY", log)),
		MaxDownloadBytes:   int64(parseInt(getEnv("MAX_DOWNLOAD_MB", log), 200)) * 1024 * 1024,
		Proxy:              strings.TrimSpace(os.Getenv("PROXY")),
	}
}

func parseAllowedHosts(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}

func parseBool(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "1" || s == "true" || s == "yes" || s == "y"
}

func parseInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
