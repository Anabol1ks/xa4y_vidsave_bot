package config

import "testing"

func TestParseAdminTelegramIDs(t *testing.T) {
	tests := []struct {
		input string
		count int
		ok    bool
	}{
		{"", 0, true},
		{" 123, 456,123 ", 2, true},
		{"0", 0, false},
		{"-1", 0, false},
		{"@admin", 0, false},
		{"12, nope", 0, false},
		{"999999999999999999999", 0, false},
	}
	for _, tc := range tests {
		ids, err := parseAdminTelegramIDs(tc.input)
		if (err == nil) != tc.ok {
			t.Errorf("parseAdminTelegramIDs(%q) err = %v", tc.input, err)
		}
		if err == nil && len(ids) != tc.count {
			t.Errorf("parseAdminTelegramIDs(%q) count = %d, want %d", tc.input, len(ids), tc.count)
		}
	}
}
