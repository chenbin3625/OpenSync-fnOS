package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServerConfig() ServerConfig {
	return ServerConfig{
		Bind:            defaultBind,
		Port:            defaultPort,
		LogLevel:        defaultLogLevel,
		ConsoleLevel:    defaultConsoleLevel,
		LogSave:         defaultLogSave,
		TaskSave:        defaultTaskSave,
		Timeout:         defaultTaskTimeout,
		CopyConcurrency: DefaultCopyConcurrency,
		ScanConcurrency: DefaultScanConcurrency,
		MaxRetries:      DefaultMaxRetries,
	}
}

// Rewriting settings must never leave a second [opensync] header behind,
// whatever shape the existing file had.
func TestWriteConfigFileKeepsOneOpensyncSection(t *testing.T) {
	cases := []struct {
		name     string
		existing string
	}{
		{"no file at all", ""},
		{"section with one managed key", "[opensync]\nport=9000\n"},
		{"section with every managed key", "[opensync]\n" + func() string {
			var b strings.Builder
			for _, k := range configManagedKeys {
				b.WriteString(k + "=1\n")
			}
			return b.String()
		}()},
		{"section followed by another section", "[opensync]\nport=9000\n\n[other]\nfoo=bar\n"},
		{"section with comments and unknown keys", "[opensync]\n# note\nport=9000\nunknown=keep\n"},
		{"empty section at EOF", "[opensync]\n"},
		{"foreign section only", "[other]\nfoo=bar\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TRIM_PKGETC", dir)
			path := filepath.Join(dir, "config.ini")
			if tc.existing != "" {
				if err := os.WriteFile(path, []byte(tc.existing), 0600); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}

			if err := writeConfigFile(testServerConfig()); err != nil {
				t.Fatalf("writeConfigFile() error: %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			content := string(data)
			if n := strings.Count(content, "[opensync]"); n != 1 {
				t.Fatalf("[opensync] appears %d times, want 1:\n%s", n, content)
			}

			// Every managed key must round-trip through the parser.
			parsed, err := readINI(path)
			if err != nil {
				t.Fatalf("readINI(%q) failed: %v", path, err)
			}
			section, ok := parsed["opensync"]
			if !ok {
				t.Fatalf("parsed file has no opensync section:\n%s", content)
			}
			for _, key := range configManagedKeys {
				if _, ok := section[key]; !ok {
					t.Fatalf("managed key %q missing after rewrite:\n%s", key, content)
				}
			}
		})
	}
}

// Content the operator added by hand must survive a settings write.
func TestWriteConfigFilePreservesForeignContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TRIM_PKGETC", dir)
	path := filepath.Join(dir, "config.ini")
	existing := "# leading comment\n[opensync]\nport=9000\nunknown=keep\n\n[other]\nfoo=bar\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := writeConfigFile(testServerConfig()); err != nil {
		t.Fatalf("writeConfigFile() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	for _, want := range []string{"# leading comment", "unknown=keep", "[other]", "foo=bar"} {
		if !strings.Contains(content, want) {
			t.Fatalf("rewrite dropped %q:\n%s", want, content)
		}
	}
	if n := strings.Count(content, "[opensync]"); n != 1 {
		t.Fatalf("[opensync] appears %d times, want 1:\n%s", n, content)
	}
}
