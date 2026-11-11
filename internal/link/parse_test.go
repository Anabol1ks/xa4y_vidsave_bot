package link

import (
	"errors"
	"testing"
)

func TestYouTubeShortsParse(t *testing.T) {
	const id = "jNQXAC9IVRw"
	tests := []struct {
		name string
		url  string
	}{
		{"shorts www", "https://www.youtube.com/shorts/" + id},
		{"shorts trailing slash", "https://youtube.com/shorts/" + id + "/"},
		{"mobile domain and tracking", "https://m.youtube.com/shorts/" + id + "?feature=share&si=abc"},
		{"short share link", "https://youtu.be/" + id + "?si=abc"},
		{"short link www", "https://www.youtu.be/" + id},
		{"case insensitive host", "https://WWW.YOUTUBE.COM/shorts/" + id},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.url, nil)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got.LinkType != TypeYouTube {
				t.Errorf("type = %q, want youtube", got.LinkType)
			}
			if got.VideoID != id {
				t.Errorf("id = %q, want %q", got.VideoID, id)
			}
			if got.Raw != tc.url {
				t.Errorf("raw = %q, want input", got.Raw)
			}
		})
	}
}

func TestYouTubeRejectsNonShorts(t *testing.T) {
	tests := []struct {
		url  string
		want error
	}{
		{"https://youtube.com/watch?v=jNQXAC9IVRw", ErrUnknownFormat},
		{"https://youtube.com/playlist?list=abcdef", ErrUnknownFormat},
		{"https://youtube.com/shorts/shortid", ErrUnknownFormat},
		{"https://youtube.com/shorts/jNQXAC9IVRw/extra", ErrUnknownFormat},
		{"https://youtu.be/notid", ErrUnknownFormat},
		{"https://youtube.com.evil.test/shorts/jNQXAC9IVRw", ErrNotAllowedHost},
		{"https://fakeyoutu.be/shorts/jNQXAC9IVRw", ErrNotAllowedHost},
		{"https://youtube.test/shorts/jNQXAC9IVRw", ErrNotAllowedHost},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			_, err := Parse(tc.url, nil)
			if !errors.Is(err, tc.want) {
				t.Errorf("Parse() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExistingPlatformsParse(t *testing.T) {
	tests := []struct {
		url      string
		platform Type
		id       string
	}{
		{"https://www.instagram.com/reel/DbJLODStVAd/", TypeInstagram, "DbJLODStVAd"},
		{"https://vm.tiktok.com/ZMtest123/", TypeTikTok, "ZMtest123"},
		{"https://www.tiktok.com/@user/video/1234567890", TypeTikTok, "1234567890"},
	}
	for _, tc := range tests {
		got, err := Parse(tc.url, nil)
		if err != nil {
			t.Errorf("%s: %v", tc.url, err)
			continue
		}
		if got.LinkType != tc.platform || got.VideoID != tc.id {
			t.Errorf("%s: got %s/%s", tc.url, got.LinkType, got.VideoID)
		}
	}
}

func TestTikTokPhotoPostsAreNotVideos(t *testing.T) {
	for _, raw := range []string{
		"https://www.tiktok.com/@isisusisp/photo/7706556456627096852?_r=1",
		"https://m.tiktok.com/@user/photo/7706556456627096852/",
	} {
		if _, err := Parse(raw, nil); !errors.Is(err, ErrTikTokPhotoPost) {
			t.Fatalf("TikTok photo post %s classified as %v", raw, err)
		}
	}
	// A vt.tiktok.com short link is accepted first; its redirect is discovered
	// by yt-dlp, which yields the precise photo-post error instead.
	p, err := Parse("https://vt.tiktok.com/ZSpaTVxRj/", nil)
	if err != nil || p.LinkType != TypeTikTok || p.VideoID != "ZSpaTVxRj" {
		t.Fatalf("short link rejected: parsed=%+v err=%v", p, err)
	}
	// Regular TikTok videos retain the exact original behavior.
	p, err = Parse("https://www.tiktok.com/@user/video/7706556456627096852", nil)
	if err != nil || p.LinkType != TypeTikTok || p.VideoID != "7706556456627096852" {
		t.Fatalf("regular video broken: parsed=%+v err=%v", p, err)
	}
}
