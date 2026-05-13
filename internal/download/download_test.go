package download

import "testing"

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
