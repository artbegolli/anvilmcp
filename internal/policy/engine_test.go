package policy

import (
	"testing"
)

func TestPathMatchesPattern(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		pattern string
		want    bool
	}{
		{
			name:    "exact match",
			path:    "/workspace/file.txt",
			pattern: "/workspace/file.txt",
			want:    true,
		},
		{
			name:    "recursive glob at end",
			path:    "/workspace/file.txt",
			pattern: "/workspace/**",
			want:    true,
		},
		{
			name:    "recursive glob with subdir",
			path:    "/workspace/subdir/file.txt",
			pattern: "/workspace/**",
			want:    true,
		},
		{
			name:    "recursive glob no match",
			path:    "/other/file.txt",
			pattern: "/workspace/**",
			want:    false,
		},
		{
			name:    "single level glob",
			path:    "/workspace/file.txt",
			pattern: "/workspace/*.txt",
			want:    true,
		},
		{
			name:    "single level glob no match",
			path:    "/workspace/subdir/file.txt",
			pattern: "/workspace/*.txt",
			want:    false,
		},
		{
			name:    "deny pattern .git/**",
			path:    "/workspace/.git/config",
			pattern: "/workspace/.git/**",
			want:    true,
		},
		{
			name:    "allow pattern workspace/**",
			path:    "/workspace/myfile.txt",
			pattern: "/workspace/**",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathMatchesPattern(tt.path, tt.pattern)
			if got != tt.want {
				t.Errorf("pathMatchesPattern(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
			}
		})
	}
}
