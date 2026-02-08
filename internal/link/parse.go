package link

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrNotURL         = errors.New("not a valid url")
	ErrNotAllowedHost = errors.New("host not allowed")
	ErrUnknownFormat  = errors.New("unknown link format")
)

type Type string

const (
	TypeTikTok    Type = "tiktok"
	TypeInstagram Type = "instagram"
)

type Parsed struct {
	Raw      string
	Scheme   string
	Host     string
	Hostname string
	Port     string
	Path     string

	LinkType Type
	VideoID  string
}

var (
	// TikTok стандартный: /@user/video/12345
	reTikTok = regexp.MustCompile(`^/@([^/]+)/video/(\d+)/?$`)
	// TikTok короткая ссылка на www/основном домене: /t/CODE
	reTikTokShort = regexp.MustCompile(`^/t/(\w+)/?$`)
	// TikTok короткая ссылка на vm/vt поддоменах: /CODE
	reTikTokVM = regexp.MustCompile(`^/(\w+)/?$`)
	// Instagram
	reInstagram = regexp.MustCompile(`^/(?:reels?|p)/([A-Za-z0-9_-]+)/?$`)
)

// tikTokShortDomains — поддомены, на которых код видео идёт прямо в корне пути.
var tikTokShortDomains = map[string]bool{
	"vm.tiktok.com": true,
	"vt.tiktok.com": true,
}

func isTikTokDomain(hostname string) bool {
	return hostname == "tiktok.com" || strings.HasSuffix(hostname, ".tiktok.com")
}

func isInstagramDomain(hostname string) bool {
	return hostname == "instagram.com" || strings.HasSuffix(hostname, ".instagram.com")
}

func Parse(raw string, allowedHosts map[string]struct{}) (Parsed, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Parsed{}, ErrNotURL
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Parsed{}, ErrNotURL
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return Parsed{}, ErrNotURL
	}

	p := Parsed{
		Raw:      raw,
		Scheme:   u.Scheme,
		Host:     strings.ToLower(u.Host),
		Hostname: strings.ToLower(u.Hostname()),
		Port:     u.Port(),
		Path:     u.Path,
	}

	// Определяем платформу по домену
	switch {
	case isTikTokDomain(p.Hostname):
		return parseTikTok(p)
	case isInstagramDomain(p.Hostname):
		return parseInstagram(p)
	}

	// Проверяем legacy allowed hosts (для совместимости)
	if isAllowed(p, allowedHosts) {
		return Parsed{}, ErrUnknownFormat
	}

	return Parsed{}, ErrNotAllowedHost
}

func parseTikTok(p Parsed) (Parsed, error) {
	// Стандартная ссылка: /@user/video/12345
	if m := reTikTok.FindStringSubmatch(p.Path); len(m) == 3 {
		p.LinkType = TypeTikTok
		p.VideoID = m[2]
		return p, nil
	}

	// Короткая ссылка на основном домене: /t/CODE
	if m := reTikTokShort.FindStringSubmatch(p.Path); len(m) == 2 {
		p.LinkType = TypeTikTok
		p.VideoID = m[1]
		return p, nil
	}

	// Короткая ссылка на vm/vt поддоменах: /CODE
	if tikTokShortDomains[p.Hostname] {
		if m := reTikTokVM.FindStringSubmatch(p.Path); len(m) == 2 {
			p.LinkType = TypeTikTok
			p.VideoID = m[1]
			return p, nil
		}
	}

	return Parsed{}, ErrUnknownFormat
}

func parseInstagram(p Parsed) (Parsed, error) {
	if m := reInstagram.FindStringSubmatch(p.Path); len(m) == 2 {
		p.LinkType = TypeInstagram
		p.VideoID = m[1]
		return p, nil
	}
	return Parsed{}, ErrUnknownFormat
}

func isAllowed(p Parsed, allowed map[string]struct{}) bool {
	if _, ok := allowed[p.Host]; ok {
		return true
	}
	if _, ok := allowed[p.Hostname]; ok {
		return true
	}
	if p.Port != "" {
		if _, ok := allowed[p.Hostname+":"+p.Port]; ok {
			return true
		}
	}
	return false
}
