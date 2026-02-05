package link

import (
	"errors"
	"log"
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
	TypeA Type = "A"
	TypeB Type = "B"
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
	reA = regexp.MustCompile(`^/@([^/]+)/video/(\d+)/?$`)
	reB = regexp.MustCompile(`^/reel/([A-Za-z0-9_-]+)/?$`)
)

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

	if !isAllowed(p, allowedHosts) {
		return Parsed{}, ErrNotAllowedHost
	}

	if m := reA.FindStringSubmatch(p.Path); len(m) == 3 {
		p.LinkType = TypeA
		p.VideoID = m[2]
		return p, nil
	}

	if m := reB.FindStringSubmatch(p.Path); len(m) == 2 {
		p.LinkType = TypeB
		p.VideoID = m[1]
		return p, nil
	}
	log.Println(p)
	return Parsed{}, ErrUnknownFormat
}

func isAllowed(p Parsed, allowed map[string]struct{}) bool {
	// 1) exact host:port (как в url.Host)
	if _, ok := allowed[p.Host]; ok {
		return true
	}
	// 2) hostname (без порта)
	if _, ok := allowed[p.Hostname]; ok {
		return true
	}
	// 3) hostname:port
	if p.Port != "" {
		if _, ok := allowed[p.Hostname+":"+p.Port]; ok {
			return true
		}
	}
	return false
}
