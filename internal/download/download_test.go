package download

import (
	"testing"

	"xa4yy_vidsave/internal/link"
)

func TestClassifyYtDlpError(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   error
	}{
		{
			name:   "auth required",
			stderr: "ERROR: [Instagram] Requested content is not available, rate-limit reached or login required. Use --cookies-from-browser or --cookies",
			want:   ErrYtDlpAuth,
		},
		{
			name:   "unsupported url",
			stderr: "ERROR: Unsupported URL: https://www.tiktok.com/@user/photo/123",
			want:   ErrYtDlpUnsupported,
		},
		{
			name:   "youtube anti-bot challenge",
			stderr: "ERROR: [youtube] Sign in to confirm you\u0027re not a bot.",
			want:   ErrYtDlpAuth,
		},
		{
			name:   "generic error",
			stderr: "ERROR: something else broke",
			want:   ErrYtDlp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyYtDlpError(tt.stderr)
			if got != tt.want {
				t.Fatalf("classifyYtDlpError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestYtDlpArgs(t *testing.T) {
	const videoURL = "https://www.youtube.com/shorts/jNQXAC9IVRw"
	cases := []struct {
		name     string
		platform link.Type
		url      string
		proxy    string
		wantArgs []string
		noArgs   []string
	}{
		{"youtube", link.TypeYouTube, videoURL, "", []string{"-f", "bv[ext=mp4][width<=1080]+ba[ext=m4a]/b[ext=mp4]", "--merge-output-format", "mp4", "--js-runtimes", "node", "--match-filter", "duration <= 180 & !is_live"}, []string{"best", "--proxy"}},
		{"instagram untouched", link.TypeInstagram, "https://instagram.com/reel/DbJLODStVAd", "", []string{"-f", "best"}, []string{"--js-runtimes", "--match-filter"}},
		{"tiktok with proxy", link.TypeTikTok, "https://www.tiktok.com/@user/video/123", "socks5h://127.0.0.1:1080", []string{"-f", "best", "--proxy", "socks5h://127.0.0.1:1080"}, []string{"--js-runtimes", "--match-filter"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := ytDlpArgs(tc.url, "/tmp/video.%(ext)s", tc.proxy, tc.platform)
			if args[len(args)-1] != tc.url {
				t.Errorf("last argument = %q, want %q", args[len(args)-1], tc.url)
			}
			for i := 0; i < len(tc.wantArgs); i += 2 {
				if !hasArgPair(args, tc.wantArgs[i], tc.wantArgs[i+1]) {
					t.Errorf("missing argument pair %q %q in %v", tc.wantArgs[i], tc.wantArgs[i+1], args)
				}
			}
			for _, forbidden := range tc.noArgs {
				for _, arg := range args {
					if arg == forbidden { t.Errorf("unexpected argument %q", forbidden) }
				}
			}
		})
	}
}

func hasArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value { return true }
	}
	return false
}
