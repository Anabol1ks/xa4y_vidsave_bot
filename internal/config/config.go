package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

type Config struct {
	BotToken                string
	AllowedHosts            map[string]struct{}
	InsecureSkipVerify      bool
	MaxDownloadBytes        int64
	MaxConcurrentDownloads  int
	Proxy                   string
	DatabaseURL             string
	AdminTelegramIDs        map[int64]struct{}
	AnalyticsWidgetsEnabled bool
}

func getEnv(key string, log *zap.Logger) string {
	if val, exists := os.LookupEnv(key); exists {
		return val
	}
	log.Error("Обязательная переменная окружения не установлена", zap.String("key", key))
	panic("missing required environment variable: " + key)
}

// parseAdminTelegramIDs fails closed: unknown IDs never receive owner stats.
func parseAdminTelegramIDs(value string) (map[int64]struct{}, error) {
	ids := map[int64]struct{}{}
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		n, err := strconv.ParseInt(token, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid ADMIN_TELEGRAM_IDS entry")
		}
		ids[n] = struct{}{}
	}
	return ids, nil
}

func Load(log *zap.Logger) *Config {
	adminIDs, err := parseAdminTelegramIDs(os.Getenv("ADMIN_TELEGRAM_IDS"))
	if err != nil {
		log.Fatal("invalid admin configuration", zap.Error(err))
	}

	return &Config{
		BotToken:                strings.TrimSpace(getEnv("BOT_TOKEN", log)),
		AllowedHosts:            parseAllowedHosts(getEnv("ALLOWED_HOSTS", log)),
		InsecureSkipVerify:      parseBool(getEnv("INSECURE_SKIP_VERIFY", log)),
		MaxDownloadBytes:        int64(parseInt(getEnv("MAX_DOWNLOAD_MB", log), 200)) * 1024 * 1024,
		MaxConcurrentDownloads:  max(1, parseInt(os.Getenv("MAX_CONCURRENT_DOWNLOADS"), 3)),
		Proxy:                   strings.TrimSpace(os.Getenv("PROXY")),
		DatabaseURL:             strings.TrimSpace(getEnv("DATABASE_URL", log)),
		AdminTelegramIDs:        adminIDs,
		AnalyticsWidgetsEnabled: parseBool(os.Getenv("ANALYTICS_WIDGET_ENABLED")),
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
