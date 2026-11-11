package link

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var errUnsafeTikTokRedirect = errors.New("unsafe TikTok redirect")

// IsTikTokShort is limited to URL paths which Telegram users paste as share
// links. Never HEAD arbitrary URLs or follow untrusted hosts.
func IsTikTokShort(p Parsed) bool {
	if p.LinkType != TypeTikTok || p.IsPhoto {
		return false
	}
	if p.Hostname == "vm.tiktok.com" || p.Hostname == "vt.tiktok.com" {
		return true
	}
	return (p.Hostname == "tiktok.com" || p.Hostname == "www.tiktok.com") &&
		strings.HasPrefix(p.Path, "/t/")
}

// ResolveTikTokShort resolves only trusted TikTok redirects; if TikTok is
// unavailable it returns the unchanged parsed share URL and the legacy
// video flow can proceed. No credentials, arbitrary URL fetches or cookies.
func ResolveTikTokShort(ctx context.Context, p Parsed, transport http.RoundTripper) Parsed {
	if !IsTikTokShort(p) || p.Scheme != "https" {
		return p
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || req.URL.Scheme != "https" || !isTikTokDomain(strings.ToLower(req.URL.Hostname())) || req.URL.Port() != "" || req.URL.User != nil {
				return errUnsafeTikTokRedirect
			}
			return nil
		},
	}
	if transport != nil {
		client.Transport = transport
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.Raw, nil)
	if err != nil {
		return p
	}
	resp, err := client.Do(req)
	if err != nil {
		return p
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p
	}
	final := *resp.Request.URL
	if final.Scheme != "https" || final.Port() != "" || !isTikTokDomain(strings.ToLower(final.Hostname())) {
		return p
	}
	final.RawQuery = ""
	final.Fragment = ""
	parsed, err := Parse((&url.URL{
		Scheme: final.Scheme, Host: final.Host,
		Path: final.Path,
	}).String(), nil)
	if err == nil && parsed.LinkType == TypeTikTok && parsed.IsPhoto {
		return parsed
	}
	return p
}
