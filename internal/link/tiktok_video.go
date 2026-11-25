package link

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var canonicalTikTokVideo = regexp.MustCompile(`^/@[A-Za-z0-9._]+/video/[0-9]+/?$`)

func safeTikTokVideoURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "tiktok.com", "www.tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com":
		return true
	}
	return false
}

// ResolveTikTokVideoURL accepts only a single canonical video or a trusted
// TikTok share link. No profile, photo, arbitrary hosts or redirect targets.
func ResolveTikTokVideoURL(ctx context.Context, raw string, transport http.RoundTripper) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !safeTikTokVideoURL(u) {
		return "", false
	}
	p, err := Parse(raw, nil)
	if err != nil || p.LinkType != TypeTikTok || p.IsPhoto {
		return "", false
	}
	if IsTikTokShort(p) {
		client := &http.Client{Timeout: 8 * time.Second, Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 4 || !safeTikTokVideoURL(req.URL) {
					return errUnsafeTikTokRedirect
				}
				return nil
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, raw, nil)
		if err != nil {
			return "", false
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", false
		}
		u = resp.Request.URL
	}
	if !safeTikTokVideoURL(u) || !canonicalTikTokVideo.MatchString(u.Path) {
		return "", false
	}
	return (&url.URL{Scheme: "https", Host: "www.tiktok.com", Path: strings.TrimSuffix(u.Path, "/")}).String(), true
}
