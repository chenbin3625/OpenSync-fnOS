package service

import (
	"bytes"
	"io"
	"log"
	"opensync/internal/config"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	appLogRingCapacity = 2000
	appLogMaxFileTail  = 2 << 20 // 2 MiB max tail read from app.log
	appLogDefaultLines = 200
	appLogMaxLines     = 2000
)

type logRingBuffer struct {
	mu      sync.Mutex
	lines   []string
	pending string
	max     int
}

func newLogRingBuffer(maxLines int) *logRingBuffer {
	if maxLines <= 0 {
		maxLines = appLogRingCapacity
	}
	return &logRingBuffer{
		lines: make([]string, 0, maxLines),
		max:   maxLines,
	}
}

func (b *logRingBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	text := b.pending + string(p)
	parts := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		b.pending = ""
		parts = parts[:len(parts)-1]
	} else {
		b.pending = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	for _, line := range parts {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if len(b.lines) >= b.max {
			copy(b.lines, b.lines[1:])
			b.lines[len(b.lines)-1] = line
		} else {
			b.lines = append(b.lines, line)
		}
	}
	return len(p), nil
}

func (b *logRingBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines), len(b.lines)+1)
	copy(out, b.lines)
	if strings.TrimSpace(b.pending) != "" {
		out = append(out, strings.TrimRight(b.pending, "\r"))
	}
	return out
}

var (
	appLogRing     = newLogRingBuffer(appLogRingCapacity)
	appLogInitOnce sync.Once
	appLogTeeOut   io.Writer = os.Stderr
)

// SetupAppLogOutput tees the standard logger output into the in-memory ring
// buffer while preserving os.Stderr output (which fnOS redirects to app.log).
func SetupAppLogOutput() io.Writer {
	appLogInitOnce.Do(func() {
		appLogTeeOut = io.MultiWriter(os.Stderr, appLogRing)
		log.SetOutput(appLogTeeOut)
	})
	return appLogTeeOut
}

// AppLogWriter returns the shared multi-writer that mirrors into the log ring.
func AppLogWriter() io.Writer {
	return SetupAppLogOutput()
}

var (
	logBearerPattern   = regexp.MustCompile(`(?i)((?:authorization\s*[:=]\s*)?bearer\s+|authorization\s*[:=]\s*|x-gotify-key\s*[:=]\s*)([^\s"',;]+)`)
	logKeyValuePattern = regexp.MustCompile(`(?i)((?:access_token|token|secret|password|passwd|sendkey|apikey|api_key|key|sign|bot_token)\s*[:=]\s*)([^\s"',;&]+)`)
	logTelegramPattern = regexp.MustCompile(`(bot\d+:)[A-Za-z0-9_-]{8,}`)
)

// redactLogLine masks tokens, passwords, and secrets before exposing logs in API/UI.
func redactLogLine(line string) string {
	if line == "" {
		return ""
	}
	s := logTelegramPattern.ReplaceAllString(line, "${1}***")
	s = logBearerPattern.ReplaceAllString(s, "${1}***")
	s = logKeyValuePattern.ReplaceAllString(s, "${1}***")
	return s
}

func classifyLogLevel(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "[ERR") ||
		strings.Contains(upper, " ERROR") ||
		strings.Contains(upper, "ERROR:") ||
		strings.Contains(upper, "FATAL") ||
		strings.Contains(upper, "PANIC") ||
		strings.Contains(upper, "FAILED") ||
		strings.Contains(line, "失败") ||
		strings.Contains(line, "异常"):
		return "error"
	case strings.Contains(upper, "[WARN") ||
		strings.Contains(upper, "WARNING") ||
		strings.Contains(upper, " WARN ") ||
		strings.Contains(line, "警告"):
		return "warn"
	default:
		return "info"
	}
}

func readAppLogFileLines(path string) ([]string, int64, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return nil, 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false
	}
	defer f.Close()

	size := info.Size()
	start := int64(0)
	truncated := false
	if size > appLogMaxFileTail {
		start = size - appLogMaxFileTail
		truncated = true
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, 0, false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, appLogMaxFileTail))
	if err != nil {
		return nil, 0, false
	}
	if truncated {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 && idx+1 < len(data) {
			data = data[idx+1:]
		}
	}
	rawLines := strings.Split(string(data), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, size, true
}

// GetAppLogs returns filtered, redacted application log lines from DataDir()/app.log
// (falling back to the in-memory log ring buffer when app.log is absent).
func GetAppLogs(lines int, level, keyword string) map[string]interface{} {
	if lines <= 0 {
		lines = appLogDefaultLines
	}
	if lines > appLogMaxLines {
		lines = appLogMaxLines
	}
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "all" {
		level = ""
	}
	keyword = strings.TrimSpace(keyword)
	keywordLower := strings.ToLower(keyword)

	logPath := filepath.Join(config.DataDir(), "app.log")
	rawLines, fileSize, fromFile := readAppLogFileLines(logPath)
	source := "file"
	if !fromFile || len(rawLines) == 0 {
		rawLines = appLogRing.Snapshot()
		source = "memory"
	}

	matched := make([]string, 0, min(len(rawLines), lines))
	for _, raw := range rawLines {
		redacted := redactLogLine(raw)
		if level != "" && classifyLogLevel(redacted) != level {
			continue
		}
		if keywordLower != "" && !strings.Contains(strings.ToLower(redacted), keywordLower) {
			continue
		}
		matched = append(matched, redacted)
	}
	total := len(matched)
	if len(matched) > lines {
		matched = matched[len(matched)-lines:]
	}
	return map[string]interface{}{
		"lines":    matched,
		"total":    total,
		"source":   source,
		"fileSize": fileSize,
	}
}

// ExportAppLogs returns filtered, redacted logs joined by newline for download.
func ExportAppLogs(lines int, level, keyword string) string {
	if lines <= 0 {
		lines = appLogMaxLines
	}
	data := GetAppLogs(lines, level, keyword)
	list, _ := data["lines"].([]string)
	if len(list) == 0 {
		return ""
	}
	return strings.Join(list, "\n") + "\n"
}
