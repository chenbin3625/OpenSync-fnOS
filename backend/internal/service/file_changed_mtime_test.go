package service

import "testing"

// Size + MD5 stay authoritative; mtime is only consulted when no usable digest
// is available on both sides. Many AList drivers never return a hash, and
// without this a same-size in-place edit would never be re-synced.
func TestFileChangedFallsBackToModifiedTime(t *testing.T) {
	const md5A = "0123456789abcdef0123456789abcdef"
	const md5B = "fedcba9876543210fedcba9876543210"

	cases := []struct {
		name     string
		src, dst FileMetadata
		want     bool
	}{
		{
			name: "different size always changed",
			src:  FileMetadata{Size: 20, Modified: 1},
			dst:  FileMetadata{Size: 10, Modified: 99},
			want: true,
		},
		{
			name: "matching digests win over newer source mtime",
			src:  FileMetadata{Size: 10, MD5: md5A, Modified: 500},
			dst:  FileMetadata{Size: 10, MD5: md5A, Modified: 100},
			want: false,
		},
		{
			name: "differing digests are changed",
			src:  FileMetadata{Size: 10, MD5: md5A, Modified: 100},
			dst:  FileMetadata{Size: 10, MD5: md5B, Modified: 100},
			want: true,
		},
		{
			name: "no digest and newer source is changed",
			src:  FileMetadata{Size: 10, Modified: 500},
			dst:  FileMetadata{Size: 10, Modified: 100},
			want: true,
		},
		{
			name: "no digest and equal mtime is unchanged",
			src:  FileMetadata{Size: 10, Modified: 100},
			dst:  FileMetadata{Size: 10, Modified: 100},
			want: false,
		},
		{
			name: "newer destination does not re-copy",
			src:  FileMetadata{Size: 10, Modified: 100},
			dst:  FileMetadata{Size: 10, Modified: 500},
			want: false,
		},
		{
			name: "missing source mtime falls back to unchanged",
			src:  FileMetadata{Size: 10},
			dst:  FileMetadata{Size: 10, Modified: 100},
			want: false,
		},
		{
			name: "missing destination mtime falls back to unchanged",
			src:  FileMetadata{Size: 10, Modified: 100},
			dst:  FileMetadata{Size: 10},
			want: false,
		},
		{
			name: "placeholder digest does not mask a newer source",
			src:  FileMetadata{Size: 10, MD5: "not-a-digest", Modified: 500},
			dst:  FileMetadata{Size: 10, MD5: "not-a-digest", Modified: 100},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileChanged(tc.src, tc.dst); got != tc.want {
				t.Fatalf("fileChanged(%+v, %+v) = %v, want %v", tc.src, tc.dst, got, tc.want)
			}
		})
	}
}

func TestToFileMetadataReadsModifiedFromMap(t *testing.T) {
	got := toFileMetadata(map[string]interface{}{
		"size":     int64(42),
		"md5":      "ABCDEF0123456789abcdef0123456789",
		"modified": int64(1700000000),
	})
	if got.Size != 42 {
		t.Fatalf("Size = %d, want 42", got.Size)
	}
	if got.MD5 != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("MD5 = %q, want lowercased digest", got.MD5)
	}
	if got.Modified != 1700000000 {
		t.Fatalf("Modified = %d, want 1700000000", got.Modified)
	}
}
