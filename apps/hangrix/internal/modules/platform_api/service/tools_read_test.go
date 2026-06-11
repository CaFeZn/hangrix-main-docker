package service

import (
	"testing"
	"time"

	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

func TestTruncateBody(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			maxRunes: 140,
			want:     "hello",
		},
		{
			name:     "exact fit no truncation",
			input:    "12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
			maxRunes: 140,
			want:     "12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
		},
		{
			name:     "ASCII truncation with suffix",
			input:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 146 'a's
			maxRunes: 140,
			// budget = 140 - 13 (suffix) = 127
			want: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + truncateSuffix,
		},
		{
			name:     "Unicode rune-aware truncation",
			input:    "你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界", // 150 runes
			maxRunes: 140,
			// budget = 140 - 13 = 127, 127 = 31*4 + 3 = "你好世界"*31 + "你好世"
			want: "你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世" + truncateSuffix,
		},
		{
			name:     "empty string unchanged",
			input:    "",
			maxRunes: 140,
			want:     "",
		},
		{
			name:     "zero maxRunes drops suffix",
			input:    "hello world",
			maxRunes: 0,
			want:     "",
		},
		{
			name:     "small maxRunes with multi-byte UTF-8 (budget < 0 path)",
			input:    "你好世界你好世界", // 8 runes, 24 bytes
			maxRunes: 3,          // budget = 3-13 = -10 → fallback path
			want:     "你好世",      // first 3 runes, no suffix
		},
		{
			name:     "result never exceeds maxRunes",
			input:    "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEF",
			maxRunes: 140,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateBody(tc.input, tc.maxRunes)
			if tc.want != "" && got != tc.want {
				t.Errorf("truncateBody(%q, %d) = %q; want %q", tc.input, tc.maxRunes, got, tc.want)
			}
			if tc.name == "result never exceeds maxRunes" {
				if len([]rune(got)) > tc.maxRunes {
					t.Errorf("truncateBody(%q, %d) returned %d runes, exceeding maxRunes limit", tc.input, tc.maxRunes, len([]rune(got)))
				}
			}
		})
	}
}

func TestCommentToDTO(t *testing.T) {
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   *issuedomain.Comment
		want *apiComment
	}{
		{
			name: "human author sets Author field",
			in: &issuedomain.Comment{
				ID: 1, IssueID: 10, AuthorID: 100,
				AuthorName: "alice", AgentRole: "",
				Body: "hello", FilePath: "", Line: 0,
				CreatedAt: now,
			},
			want: &apiComment{
				ID: 1, Author: "alice", AgentRole: "",
				Body: "hello", FilePath: "", Line: 0,
				CreatedAt: "2026-05-29T08:00:00Z",
			},
		},
		{
			name: "agent author sets AgentRole as Author",
			in: &issuedomain.Comment{
				ID: 2, IssueID: 10, AuthorID: 200,
				AuthorName: "bot", AgentRole: "tester",
				Body: "review: looks good", FilePath: "main.go", Line: 42,
				CreatedAt: now,
			},
			want: &apiComment{
				ID: 2, Author: "tester", AgentRole: "tester",
				Body: "review: looks good", FilePath: "main.go", Line: 42,
				CreatedAt: "2026-05-29T08:00:00Z",
			},
		},
		{
			name: "zero CreatedAt yields empty string",
			in: &issuedomain.Comment{
				ID: 3, IssueID: 10, AuthorID: 100,
				AuthorName: "alice", AgentRole: "",
				Body: "no timestamp",
			},
			want: &apiComment{
				ID: 3, Author: "alice", AgentRole: "",
				Body: "no timestamp", FilePath: "", Line: 0,
				CreatedAt: "",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commentToDTO(tc.in)
			if got.ID != tc.want.ID || got.Author != tc.want.Author ||
				got.AgentRole != tc.want.AgentRole || got.Body != tc.want.Body ||
				got.FilePath != tc.want.FilePath || got.Line != tc.want.Line ||
				got.CreatedAt != tc.want.CreatedAt {
				t.Errorf("commentToDTO mismatch:\n  got:  %+v\n  want: %+v", got, tc.want)
			}
		})
	}
}
