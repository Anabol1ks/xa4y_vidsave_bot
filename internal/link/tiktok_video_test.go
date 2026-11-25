package link

import (
	"context"
	"testing"
)

func TestResolveTikTokVideoURL(t *testing.T) {
	ctx := context.Background()
	want := "https://www.tiktok.com/@u/video/123"
	for _, raw := range []string{
		"https://www.tiktok.com/@u/video/123?x=1",
		"https://m.tiktok.com/@u/video/123/",
		"https://vt.tiktok.com/ZSabc/",
	} {
		got, ok := ResolveTikTokVideoURL(ctx, raw, testRedirectTransport(want+"?_t=1"))
		if !ok || got != want {
			t.Fatalf("%q -> %q, %v", raw, got, ok)
		}
	}
	for _, raw := range []string{
		"https://u:pw@www.tiktok.com/@u/video/123",
		"https://www.tiktok.com:443/@u/video/123",
		"http://www.tiktok.com/@u/video/123",
		"https://not-tiktok.com/@u/video/123",
		"https://evil.tiktok.com/@u/video/123",
		"https://www.tiktok.com/@u/photo/123",
		"https://www.tiktok.com/@u",
	} {
		if got, ok := ResolveTikTokVideoURL(ctx, raw, nil); ok {
			t.Fatalf("unsafe URL %q accepted: %s", raw, got)
		}
	}
	for _, target := range []string{
		"https://www.tiktok.com/@u/photo/123",
		"https://www.tiktok.com/@u",
		"https://example.com/@u/video/123",
		"http://www.tiktok.com/@u/video/123",
		"https://www.tiktok.com:443/@u/video/123",
		"https://user@www.tiktok.com/@u/video/123",
	} {
		if got, ok := ResolveTikTokVideoURL(ctx, "https://vt.tiktok.com/ZSabc/", testRedirectTransport(target)); ok {
			t.Fatalf("unsafe redirect %q accepted: %s", target, got)
		}
	}
}
