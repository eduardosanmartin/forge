package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/logging"
)

const (
	testAWSKey  = "AKIAIOSFODNN7EXAMPLE"                 // AKIA + 16 chars
	testOpenAI  = "sk-proj-abcdefghijklmnop1234"         // sk- + >16 chars
	testGitHub  = "ghp_0123456789abcdefghijklmnopqrstuv" // gh[pousr]_ + >=20 chars
	testPEMHead = "-----BEGIN RSA PRIVATE KEY-----"      // PEM header marker
	testLongVal = "supersecretvalue123456"               // generic assignment value
)

func TestRedactEveryPatternClass(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "aws access key",
			in:   "using key " + testAWSKey + " today",
			want: "using key [REDACTED] today",
		},
		{
			name: "private key pem header",
			in:   testPEMHead + "\nMIIEpAIBAAKCAQEA",
			want: "[REDACTED]\nMIIEpAIBAAKCAQEA",
		},
		{
			name: "openai style key",
			in:   "auth with " + testOpenAI,
			want: "auth with [REDACTED]",
		},
		{
			name: "github token",
			in:   "push using " + testGitHub,
			want: "push using [REDACTED]",
		},
		{
			name: "generic assignment equals form",
			in:   "api_key=" + testLongVal,
			want: "api_key=[REDACTED]",
		},
		{
			name: "generic assignment colon quoted form",
			in:   `password: "` + testLongVal + `"`,
			want: "password: [REDACTED]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := logging.Redact(tc.in); got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactBenignTextUntouched(t *testing.T) {
	cases := []string{
		"http://127.0.0.1:11434/v1/chat/completions",
		"https://api.example.com/models",
		"loaded 3 providers from config",
		"tokens are cheap until they are not", // keyword without assignment separator
		"token=x",                             // value shorter than 8 chars
		"",
	}
	for _, in := range cases {
		if got := logging.Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactCaseInsensitiveAssignment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"API_KEY: " + testLongVal, "API_KEY: [REDACTED]"},
		{"Authorization:" + testLongVal, "Authorization:[REDACTED]"},
		{"TOKEN = \"" + testLongVal + "\"", `TOKEN = [REDACTED]`},
	}
	for _, tc := range cases {
		if got := logging.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactMultipleOccurrences(t *testing.T) {
	in := "a=token1 token: " + testLongVal + " b=" + testAWSKey
	got := logging.Redact(in)
	if strings.Contains(got, testAWSKey) || strings.Contains(got, testLongVal) {
		t.Errorf("Redact left secrets behind: %q", got)
	}
	if !strings.Contains(got, "token: [REDACTED]") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("Redact output lost expected placeholders: %q", got)
	}
}

func TestAddRedactPatternsExtendsBehavior(t *testing.T) {
	custom := regexp.MustCompile(`FORGE-[0-9]{12}`)
	logging.AddRedactPatterns(custom)

	in := "lease FORGE-000000000001 expires soon"
	if got := logging.Redact(in); !strings.Contains(got, "[REDACTED]") || strings.Contains(got, "FORGE-000000000001") {
		t.Errorf("custom pattern not applied: %q", got)
	}
}

// decodeRecord parses a single JSON log line into a generic map.
func decodeRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("decode log record %q: %v", buf.String(), err)
	}
	return rec
}

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(logging.NewJSONHandler(buf, slog.LevelDebug)), buf
}

func TestLoggerRedactsMessagesAndAttrs(t *testing.T) {
	logger, buf := newTestLogger()
	logger.Info("leak "+testAWSKey+" in message", "authorization", "Bearer "+testOpenAI)

	rec := decodeRecord(t, buf)
	msg, _ := rec["msg"].(string)
	if msg != "leak [REDACTED] in message" {
		t.Errorf("msg = %q, want redacted message", msg)
	}
	auth, _ := rec["authorization"].(string)
	if auth != "Bearer [REDACTED]" {
		t.Errorf(`attr authorization = %q, want "Bearer [REDACTED]"`, auth)
	}
	for _, secret := range []string{testAWSKey, testOpenAI} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("raw secret %q leaked into log output", secret)
		}
	}
}

func TestLoggerRedactsGroupsAndPreformattedAttrs(t *testing.T) {
	logger, buf := newTestLogger()
	logger.WithGroup("req").Info("status ok", "url", "http://127.0.0.1:11434/v1", "password", testAWSKey)

	rec := decodeRecord(t, buf)
	req, ok := rec["req"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested group req in %v", rec)
	}
	if url, _ := req["url"].(string); url != "http://127.0.0.1:11434/v1" {
		t.Errorf("benign URL attr was altered: %q", url)
	}
	if pw, _ := req["password"].(string); pw != "[REDACTED]" {
		t.Errorf("grouped password attr = %q, want [REDACTED]", pw)
	}
}

func TestLoggerRedactsPreformattedWithAttrs(t *testing.T) {
	logger, buf := newTestLogger()
	logger.With("api_key", testOpenAI).Info("preformatted context")

	rec := decodeRecord(t, buf)
	key, _ := rec["api_key"].(string)
	if key != "[REDACTED]" {
		t.Errorf("preformatted api_key attr = %q, want [REDACTED]", key)
	}
}

func TestLoggerPreservesNonStringAttrs(t *testing.T) {
	logger, buf := newTestLogger()
	logger.Info("counts", "attempts", 3, "ratio", 0.5, "tags", []string{"alpha", testAWSKey})

	rec := decodeRecord(t, buf)
	if v, _ := rec["attempts"].(float64); v != 3 {
		t.Errorf("attempts = %v, want 3", rec["attempts"])
	}
	tagsRaw, ok := rec["tags"].([]any)
	if !ok {
		t.Fatalf("tags = %#v, want array", rec["tags"])
	}
	first, _ := tagsRaw[0].(string)
	second, _ := tagsRaw[1].(string)
	if first != "alpha" || second != "[REDACTED]" {
		t.Errorf("tags = [%q, %q], want [alpha [REDACTED]]", first, second)
	}
}

func TestNewInvalidLevelErrors(t *testing.T) {
	cases := []string{"verbose", "", "trace"}
	for _, level := range cases {
		_, _, err := logging.New(logging.Config{Level: level})
		if err == nil {
			t.Errorf("New(level=%q) succeeded; want error", level)
			continue
		}
		if !strings.Contains(err.Error(), "supported levels") {
			t.Errorf("error for level %q = %q, should list supported levels", level, err.Error())
		}
	}
}

func TestNewValidLevelsWithoutFile(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		logger, file, err := logging.New(logging.Config{Level: level})
		if err != nil {
			t.Fatalf("New(level=%q): %v", level, err)
		}
		if file != nil {
			t.Errorf("New(level=%q) returned non-nil closer without File", level)
		}
		if logger == nil {
			t.Errorf("New(level=%q) returned nil logger", level)
		}
	}
}

func TestNewWritesToFileAndStderrIsAlwaysTargeted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge.log")

	logger, file, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if file == nil {
		t.Fatal("New returned nil closer despite File being set")
	}

	logger.Info("file sink check")
	if err := file.Close(); err != nil {
		t.Fatalf("close log file: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "file sink check") {
		t.Errorf("log file missing record; got %q", data)
	}
	if !json.Valid([]byte(strings.TrimSpace(string(data)))) {
		t.Errorf("log file content is not valid JSON: %q", data)
	}
}

func TestNewAppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge.log")

	first, f1, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	first.Info("one")
	if err := f1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, f2, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	second.Info("two")
	if err := f2.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"\"one\"", "\"two\""} {
		if !strings.Contains(string(data), want) {
			t.Errorf("appended log missing %s; got %q", want, data)
		}
	}
}
