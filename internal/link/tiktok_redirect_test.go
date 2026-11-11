package link

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testRedirectTransport(target string) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodHead {
			return nil, errors.New("only HEAD is permitted")
		}
		if req.URL.Hostname() == "vt.tiktok.com" {
			return &http.Response{StatusCode: http.StatusFound,
				Header: http.Header{"Location": []string{target}},
				Body:   io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK,
			Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
}

func TestResolveTikTokPhotoShareAndKeepVideo(t *testing.T) {
	p, err := Parse("https://vt.tiktok.com/ZSpaTVxRj/", nil)
	if err != nil {
		t.Fatal(err)
	}
	photo := ResolveTikTokShort(context.Background(), p, testRedirectTransport("https://www.tiktok.com/@isisusisp/photo/7706556456627096852?_t=test"))
	if !photo.IsPhoto || photo.VideoID != "7706556456627096852" || photo.LinkType != TypeTikTok {
		t.Fatalf("photo redirect not recognized: %+v", photo)
	}
	if photo.Raw != "https://www.tiktok.com/@isisusisp/photo/7706556456627096852" {
		t.Fatal("tracking query must be removed:", photo.Raw)
	}
	video := ResolveTikTokShort(context.Background(), p, testRedirectTransport("https://www.tiktok.com/@u/video/1234567890"))
	if video.IsPhoto || video.Raw != p.Raw {
		t.Fatal("regular TikTok video must stay unchanged", video)
	}
}

func TestTikTokPhotoDirectAndHostSafety(t *testing.T) {
	for _, raw := range []string{
		"https://www.tiktok.com/@isisusisp/photo/7706556456627096852",
		"https://m.tiktok.com/@friend/photo/1234567890123456789?foo=bar",
	} {
		p, err := Parse(raw, nil)
		if err != nil || !p.IsPhoto || p.LinkType != TypeTikTok {
			t.Fatalf("direct photo parsing: %+v %v", p, err)
		}
	}
	for _, target := range []string{
		"https://not-tiktok.test/@user/photo/123",
		"http://www.tiktok.com/@user/photo/123",
		"https://localhost:18873/private",
		"https://www.tiktok.com:444/@user/photo/123",
	} {
		p, _ := Parse("https://vt.tiktok.com/ZSpaTVxRj/", nil)
		got := ResolveTikTokShort(context.Background(), p, testRedirectTransport(target))
		if got.Raw != p.Raw || got.IsPhoto {
			t.Fatalf("followed unsafe target %s", target)
		}
	}
}
