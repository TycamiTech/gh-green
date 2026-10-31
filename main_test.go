package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	envFile := filepath.Join(tempDir, ".env")

	envContent := `# Sample env file
GH_TOKEN="ghp_test_token_123456"
export GH_OWNER='testowner' # with trailing comment
GH_REPO=test-repo
GH_BRANCH=develop
SKIP_PROBABILITY=25
MAX_COMMITS=7
MAX_JITTER_MINUTES=45
# Empty line next

INVALID_LINE_WITHOUT_EQUALS
QUOTED_VAL="hello world with spaces"
`
	if err := os.WriteFile(envFile, []byte(envContent), 0644); err != nil {
		t.Fatalf("failed to write test env: %v", err)
	}

	logger := NewLogger()
	// Clear existing env
	os.Unsetenv("GH_TOKEN")
	os.Unsetenv("GH_OWNER")
	os.Unsetenv("GH_REPO")
	os.Unsetenv("GH_BRANCH")
	os.Unsetenv("SKIP_PROBABILITY")
	os.Unsetenv("MAX_COMMITS")
	os.Unsetenv("MAX_JITTER_MINUTES")
	os.Unsetenv("QUOTED_VAL")

	if err := LoadDotEnv(envFile, logger); err != nil {
		t.Fatalf("LoadDotEnv failed: %v", err)
	}

	if val := os.Getenv("GH_TOKEN"); val != "ghp_test_token_123456" {
		t.Errorf("expected GH_TOKEN 'ghp_test_token_123456', got %q", val)
	}
	if val := os.Getenv("GH_OWNER"); val != "testowner" {
		t.Errorf("expected GH_OWNER 'testowner', got %q", val)
	}
	if val := os.Getenv("GH_REPO"); val != "test-repo" {
		t.Errorf("expected GH_REPO 'test-repo', got %q", val)
	}
	if val := os.Getenv("GH_BRANCH"); val != "develop" {
		t.Errorf("expected GH_BRANCH 'develop', got %q", val)
	}
	if val := os.Getenv("SKIP_PROBABILITY"); val != "25" {
		t.Errorf("expected SKIP_PROBABILITY '25', got %q", val)
	}
	if val := os.Getenv("QUOTED_VAL"); val != "hello world with spaces" {
		t.Errorf("expected QUOTED_VAL 'hello world with spaces', got %q", val)
	}
}

func TestDerivePackageName(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"internal/telemetry/state.go", "telemetry"},
		{"telemetry/state.go", "telemetry"},
		{"state.go", "main"},
		{"pkg/metrics-engine/state.go", "metrics_engine"},
		{"123num/state.go", "pkg_123num"},
	}

	for _, tt := range tests {
		got := DerivePackageName(tt.path)
		if got != tt.expected {
			t.Errorf("DerivePackageName(%q) = %q; want %q", tt.path, got, tt.expected)
		}
	}
}

func TestExtractIteration(t *testing.T) {
	content := `
package telemetry
var CurrentState = OperationalState{
	Iteration:       42,
	LastHeartbeat:   time.Now(),
}
`
	iter := ExtractIteration(content)
	if iter != 42 {
		t.Errorf("ExtractIteration() = %d; want 42", iter)
	}

	emptyIter := ExtractIteration("")
	if emptyIter != 0 {
		t.Errorf("ExtractIteration() on empty content = %d; want 0", emptyIter)
	}
}

func TestGenerateCodePayload(t *testing.T) {
	filePath := "internal/telemetry/state.go"
	prevContent := `Iteration: 5`

	payload, iteration := GenerateCodePayload(filePath, prevContent)
	if iteration != 6 {
		t.Errorf("expected iteration 6, got %d", iteration)
	}

	code := string(payload)
	if !strings.Contains(code, "package telemetry") {
		t.Errorf("payload missing package name: %s", code)
	}
	if !strings.Contains(code, "Iteration:       6,") {
		t.Errorf("payload missing updated iteration: %s", code)
	}
	if !strings.Contains(code, "func Snapshot() OperationalState") {
		t.Errorf("payload missing Snapshot function: %s", code)
	}
}

func TestCommitMessagesCount(t *testing.T) {
	if len(CommitMessages) < 35 {
		t.Errorf("CommitMessages count = %d; want at least 35", len(CommitMessages))
	}

	// Verify conventional commit prefixes
	validPrefixes := []string{"feat", "fix", "refactor", "perf", "chore", "docs", "test"}
	for _, msg := range CommitMessages {
		hasValidPrefix := false
		for _, prefix := range validPrefixes {
			if strings.HasPrefix(msg, prefix) {
				hasValidPrefix = true
				break
			}
		}
		if !hasValidPrefix {
			t.Errorf("commit message %q does not start with standard conventional commit prefix", msg)
		}
	}
}

func TestGitHubClientMock(t *testing.T) {
	// Mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify required headers
		if r.Header.Get("Authorization") != "Bearer mock_token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("unexpected user agent: %s", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("unexpected API version header: %s", r.Header.Get("X-GitHub-Api-Version"))
		}

		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"sha": "mock_sha_12345",
				"content": "SGVsbG8gV29ybGQ=",
				"encoding": "base64",
				"size": 11
			}`))
			return
		}

		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"content": { "sha": "new_blob_sha" },
				"commit": { "sha": "new_commit_sha", "html_url": "https://github.com/test/repo/commit/123" }
			}`))
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer mockServer.Close()

	logger := NewLogger()
	client := NewGitHubClient("mock_token", logger)
	client.httpClient = mockServer.Client()

	// Verify headers setting
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, mockServer.URL, nil)
	client.setHeaders(req)

	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Fatalf("client request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
