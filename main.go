package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// App metadata
const (
	AppName    = "gh-green"
	AppVersion = "1.0.0"
	UserAgent  = "gh-green-agent/1.0"
)

// LogLevel represents the severity of a log message.
type LogLevel string

const (
	LogLevelInfo    LogLevel = "INFO"
	LogLevelSuccess LogLevel = "SUCCESS"
	LogLevelWarn    LogLevel = "WARN"
	LogLevelError   LogLevel = "ERROR"
	LogLevelDryRun  LogLevel = "DRY-RUN"
)

// Logger handles structured console logging with RFC3339 timestamps.
type Logger struct {
	useColor bool
}

// NewLogger creates a new structured logger instance.
func NewLogger() *Logger {
	// Enable color if terminal supports it (simple check)
	useColor := os.Getenv("NO_COLOR") == ""
	return &Logger{useColor: useColor}
}

func (l *Logger) log(level LogLevel, format string, args ...any) {
	timestamp := time.Now().Format(time.RFC3339)
	msg := fmt.Sprintf(format, args...)

	levelTag := string(level)
	if l.useColor {
		switch level {
		case LogLevelInfo:
			levelTag = "\033[36mINFO\033[0m"
		case LogLevelSuccess:
			levelTag = "\033[32mSUCCESS\033[0m"
		case LogLevelWarn:
			levelTag = "\033[33mWARN\033[0m"
		case LogLevelError:
			levelTag = "\033[31mERROR\033[0m"
		case LogLevelDryRun:
			levelTag = "\033[35mDRY-RUN\033[0m"
		}
	}

	fmt.Fprintf(os.Stdout, "[%s] [%s] %s\n", timestamp, levelTag, msg)
}

func (l *Logger) Info(format string, args ...any) {
	l.log(LogLevelInfo, format, args...)
}

func (l *Logger) Success(format string, args ...any) {
	l.log(LogLevelSuccess, format, args...)
}

func (l *Logger) Warn(format string, args ...any) {
	l.log(LogLevelWarn, format, args...)
}

func (l *Logger) Error(format string, args ...any) {
	l.log(LogLevelError, format, args...)
}

func (l *Logger) DryRun(format string, args ...any) {
	l.log(LogLevelDryRun, format, args...)
}

// Config holds runtime configuration options.
type Config struct {
	Token            string
	Owner            string
	Repo             string
	Branch           string
	TargetFile       string
	SkipProbability  int
	MaxCommits       int
	MaxJitterMinutes int

	// CLI flags
	Now           bool
	ForcedCommits int
	DryRun        bool
	EnvPath       string
}

// LoadDotEnv parses a .env file and sets variables in the OS environment if unset.
func LoadDotEnv(filePath string, logger *Logger) error {
	file, err := os.Open(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// If default .env is missing, it's acceptable as long as environment is populated
			return err
		}
		return fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	loadedCount := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and full comment lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Support 'export KEY=VALUE' syntax
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		// Split on first '='
		equalIdx := strings.Index(line, "=")
		if equalIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:equalIdx])
		val := strings.TrimSpace(line[equalIdx+1:])

		// Extract value, respecting quotes and trailing comments
		if strings.HasPrefix(val, "\"") {
			if endIdx := strings.LastIndex(val[1:], "\""); endIdx != -1 {
				val = val[1 : 1+endIdx]
			}
		} else if strings.HasPrefix(val, "'") {
			if endIdx := strings.LastIndex(val[1:], "'"); endIdx != -1 {
				val = val[1 : 1+endIdx]
			}
		} else {
			if commentIdx := strings.Index(val, "#"); commentIdx != -1 {
				val = strings.TrimSpace(val[:commentIdx])
			}
		}

		if key == "" {
			continue
		}

		// Only set if not already set in OS environment
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, val); err != nil {
				logger.Warn("Failed to set env var %s: %v", key, err)
			} else {
				loadedCount++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading env file: %w", err)
	}

	logger.Info("Loaded %d variables from %s", loadedCount, filePath)
	return nil
}

// parseEnvInt helper reads an int env var or returns a fallback default.
func parseEnvInt(key string, fallback int) int {
	valStr := strings.TrimSpace(os.Getenv(key))
	if valStr == "" {
		return fallback
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return fallback
	}
	return val
}

// ResolveConfig combines CLI flags and environment variables into Config.
func ResolveConfig(logger *Logger) (*Config, error) {
	cfg := &Config{}

	flag.BoolVar(&cfg.Now, "now", false, "Bypass probability skip and jitter delay, execute immediate commit")
	flag.IntVar(&cfg.ForcedCommits, "commits", 0, "Force specific number of commits (ignores random count)")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Simulate full process and log actions without sending HTTP PUT to GitHub")
	flag.StringVar(&cfg.EnvPath, "env", ".env", "Path to .env configuration file")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s v%s:\n\n", AppName, AppVersion)
		fmt.Fprintf(os.Stderr, "Automated GitHub commit tool with natural human simulation.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nEnvironment Variables:\n")
		fmt.Fprintf(os.Stderr, "  GH_TOKEN            Required. GitHub Personal Access Token (contents:write)\n")
		fmt.Fprintf(os.Stderr, "  GH_OWNER            Required. GitHub username or organization\n")
		fmt.Fprintf(os.Stderr, "  GH_REPO             Required. Target repository name\n")
		fmt.Fprintf(os.Stderr, "  GH_BRANCH           Optional. Target branch (default: main)\n")
		fmt.Fprintf(os.Stderr, "  GH_TARGET_FILE      Optional. Target file path in repo (default: internal/telemetry/state.go)\n")
		fmt.Fprintf(os.Stderr, "  SKIP_PROBABILITY    Optional. Skip percentage chance 0-100 (default: 15)\n")
		fmt.Fprintf(os.Stderr, "  MAX_COMMITS         Optional. Maximum commits per run (default: 5)\n")
		fmt.Fprintf(os.Stderr, "  MAX_JITTER_MINUTES  Optional. Maximum initial jitter wait in minutes (default: 90)\n")
	}

	flag.Parse()

	// Load .env file
	if err := LoadDotEnv(cfg.EnvPath, logger); err != nil {
		if !errors.Is(err, os.ErrNotExist) || cfg.EnvPath != ".env" {
			logger.Warn("Notice: could not load env file %q: %v", cfg.EnvPath, err)
		}
	}

	// Read required and optional environment variables
	cfg.Token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	cfg.Owner = strings.TrimSpace(os.Getenv("GH_OWNER"))
	cfg.Repo = strings.TrimSpace(os.Getenv("GH_REPO"))

	cfg.Branch = strings.TrimSpace(os.Getenv("GH_BRANCH"))
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}

	cfg.TargetFile = strings.TrimSpace(os.Getenv("GH_TARGET_FILE"))
	if cfg.TargetFile == "" {
		cfg.TargetFile = "internal/telemetry/state.go"
	}
	// Normalize target file path separators for GitHub REST API (must be forward slashes)
	cfg.TargetFile = strings.ReplaceAll(cfg.TargetFile, "\\", "/")
	cfg.TargetFile = strings.TrimPrefix(cfg.TargetFile, "/")

	cfg.SkipProbability = parseEnvInt("SKIP_PROBABILITY", 15)
	if cfg.SkipProbability < 0 {
		cfg.SkipProbability = 0
	} else if cfg.SkipProbability > 100 {
		cfg.SkipProbability = 100
	}

	cfg.MaxCommits = parseEnvInt("MAX_COMMITS", 5)
	if cfg.MaxCommits < 1 {
		cfg.MaxCommits = 1
	}

	cfg.MaxJitterMinutes = parseEnvInt("MAX_JITTER_MINUTES", 90)
	if cfg.MaxJitterMinutes < 0 {
		cfg.MaxJitterMinutes = 0
	}

	// Validate required variables (unless in dry-run mode where user might test flags)
	var missing []string
	if cfg.Token == "" {
		missing = append(missing, "GH_TOKEN")
	}
	if cfg.Owner == "" {
		missing = append(missing, "GH_OWNER")
	}
	if cfg.Repo == "" {
		missing = append(missing, "GH_REPO")
	}

	if len(missing) > 0 {
		if cfg.DryRun {
			logger.Warn("Missing required environment variables: %s (continuing because --dry-run is active)", strings.Join(missing, ", "))
			if cfg.Token == "" {
				cfg.Token = "ghp_dryrun_dummy_token_value_00000000000"
			}
			if cfg.Owner == "" {
				cfg.Owner = "octocat"
			}
			if cfg.Repo == "" {
				cfg.Repo = "telemetry-sandbox"
			}
		} else {
			return nil, fmt.Errorf("missing required environment variables: %s. Please set them or supply a valid .env file", strings.Join(missing, ", "))
		}
	}

	return cfg, nil
}

// GitHubClient handles HTTP operations against the GitHub REST API.
type GitHubClient struct {
	token      string
	httpClient *http.Client
	logger     *Logger
}

// NewGitHubClient constructs a new GitHubClient with a 20-second timeout.
func NewGitHubClient(token string, logger *Logger) *GitHubClient {
	return &GitHubClient{
		token: token,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		logger: logger,
	}
}

// FileContentResponse models the response from GitHub GET /repos/{owner}/{repo}/contents/{path}.
type FileContentResponse struct {
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Size     int64  `json:"size"`
}

// PutContentPayload models the request body for GitHub PUT /repos/{owner}/{repo}/contents/{path}.
type PutContentPayload struct {
	Message string  `json:"message"`
	Content string  `json:"content"`
	Branch  string  `json:"branch"`
	SHA     *string `json:"sha,omitempty"`
}

// PutContentResponse models the response from GitHub PUT /repos/{owner}/{repo}/contents/{path}.
type PutContentResponse struct {
	Content struct {
		SHA  string `json:"sha"`
		Path string `json:"path"`
	} `json:"content"`
	Commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Message string `json:"message"`
	} `json:"commit"`
}

func (c *GitHubClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
}

// GetFileContents retrieves the file info and current SHA from GitHub.
// Returns sha="", content="", exists=false if HTTP 404 is encountered.
func (c *GitHubClient) GetFileContents(ctx context.Context, owner, repo, filePath, branch string) (sha string, rawContent []byte, exists bool, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s", owner, repo, filePath, branch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, false, fmt.Errorf("create request failed: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, false, fmt.Errorf("http GET failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, false, fmt.Errorf("read response body failed: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", nil, false, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, false, fmt.Errorf("GitHub API GET error [HTTP %d]: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var res FileContentResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return "", nil, false, fmt.Errorf("unmarshal GitHub response failed: %w", err)
	}

	var decoded []byte
	if res.Encoding == "base64" && res.Content != "" {
		cleaned := strings.ReplaceAll(res.Content, "\n", "")
		cleaned = strings.ReplaceAll(cleaned, "\r", "")
		decoded, _ = base64.StdEncoding.DecodeString(cleaned)
	}

	return res.SHA, decoded, true, nil
}

// PutFileContents updates or creates a file in the repository.
func (c *GitHubClient) PutFileContents(ctx context.Context, owner, repo, filePath, branch, message string, content []byte, existingSHA string) (*PutContentResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, filePath)

	encodedContent := base64.StdEncoding.EncodeToString(content)
	payload := PutContentPayload{
		Message: message,
		Content: encodedContent,
		Branch:  branch,
	}
	if existingSHA != "" {
		payload.SHA = &existingSHA
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal put payload failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("create put request failed: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http PUT failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("GitHub API PUT error [HTTP %d]: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var putResp PutContentResponse
	if err := json.Unmarshal(bodyBytes, &putResp); err != nil {
		return nil, fmt.Errorf("unmarshal put response failed: %w", err)
	}

	return &putResp, nil
}

// CommitMessages contains realistic Conventional Commits variations for backend/systems engineering.
var CommitMessages = []string{
	"feat(telemetry): implement adaptive metrics sampling window",
	"fix(buffer): resolve race condition in concurrent ring buffer flush",
	"refactor(pipeline): decouple ingestion dispatcher from worker pool",
	"perf(alloc): optimize byte buffer pool recycling in hot path",
	"chore(deps): synchronize internal runtime state definitions",
	"docs(architecture): update telemetry state serialization specification",
	"test(telemetry): add table-driven tests for state serializer",
	"feat(collector): add exponential backoff for transient network errors",
	"fix(drain): prevent goroutine leak during graceful worker shutdown",
	"perf(json): eliminate redundant memory allocations during serialization",
	"refactor(metrics): reorganize telemetry counter aggregators",
	"chore(internal): bump internal build schema version and heartbeat",
	"feat(health): introduce health check probe latch for background workers",
	"fix(sync): fix double-locking edge case in state mutex lock",
	"perf(hash): switch to 64-bit FNV-1a checksum for rapid state verification",
	"chore(runtime): refresh operational telemetry pulse snapshot",
	"feat(stream): add batching threshold for high-throughput stream buffer",
	"docs(telemetry): document state recovery invariants and lifecycle",
	"refactor(config): streamline internal telemetry options validation",
	"fix(timestamp): normalize monotonic clock drift in snapshot generator",
	"test(pipeline): increase test coverage for edge-case payload sizes",
	"perf(compress): tune compression level for state ledger checkpoints",
	"chore(cleanup): remove deprecated telemetry field stubs",
	"feat(limiter): implement token bucket rate limiter for heartbeat pulses",
	"fix(worker): handle unexpected EOF during cold state warm-up",
	"refactor(transport): extract reusable HTTP client round-tripper options",
	"docs(readme): clarify telemetry state schema and invariants",
	"test(fuzz): add continuous fuzzing target for telemetry payload decoder",
	"perf(cache): optimize LRU cache eviction policy for metric keys",
	"feat(tracing): attach span contexts to distributed worker tasks",
	"fix(retry): correct retry count decrementation under heavy load",
	"chore(metrics): update default telemetry emission frequency constants",
	"refactor(errors): wrap internal errors with contextual stack information",
	"perf(io): utilize zero-copy reader interface for payload streaming",
	"feat(telemetry): persist high-water mark sequence for checkpoint recovery",
	"fix(overflow): guard against uint64 sequence counter wraparound",
	"chore(telemetry): update state heartbeat and operational metrics",
	"perf(atomic): replace RWMutex with atomic pointer for state reads",
	"feat(monitor): add resource utilization gauge to periodic heartbeat",
	"refactor(state): consolidate state snapshot encoding routines",
}

// CryptoRandomInt returns a cryptographically secure uniform random integer in [0, max).
func CryptoRandomInt(max int) int {
	if max <= 0 {
		return 0
	}
	nBig, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		// Fallback to time-seeded pseudo randomness if crypto/rand fails
		return int(time.Now().UnixNano() % int64(max))
	}
	return int(nBig.Int64())
}

// GenerateRandomHex produces a random hex string of given byte length.
func GenerateRandomHex(numBytes int) string {
	b := make([]byte, numBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ExtractIteration attempts to extract the previous iteration number from existing code content.
func ExtractIteration(content string) int64 {
	re := regexp.MustCompile(`Iteration:\s*(\d+)`)
	matches := re.FindStringSubmatch(content)
	if len(matches) > 1 {
		if val, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			return val
		}
	}
	return 0
}

// DerivePackageName determines an idiomatic Go package name from the target file path.
func DerivePackageName(filePath string) string {
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return "main"
	}
	base := path.Base(dir)
	return sanitizeGoIdent(base)
}

func sanitizeGoIdent(s string) string {
	s = strings.ToLower(s)
	reg := regexp.MustCompile(`[^a-z0-9_]`)
	s = reg.ReplaceAllString(s, "_")
	if len(s) == 0 || (s[0] >= '0' && s[0] <= '9') {
		s = "pkg_" + s
	}
	return s
}

// GenerateCodePayload produces syntactically valid Go code conforming to 100% Go Linguist detection.
func GenerateCodePayload(filePath string, previousContent string) (content []byte, iteration int64) {
	pkgName := DerivePackageName(filePath)
	prevIter := ExtractIteration(previousContent)
	iteration = prevIter + 1

	now := time.Now()
	unixSec := now.Unix()
	unixNsec := now.Nanosecond()
	rfc3339 := now.Format(time.RFC3339)

	buildHash := GenerateRandomHex(16)
	entropySeed := GenerateRandomHex(8)
	activeWorkers := 4 + CryptoRandomInt(13)               // 4..16 workers
	memoryAllocated := 1048576 + CryptoRandomInt(7340032) // 1MB..8MB

	code := fmt.Sprintf(`// Code generated by gh-green agent. DO NOT EDIT.
// Telemetry operational state and runtime metrics snapshot.

package %s

import (
	"time"
)

const (
	// SchemaVersion represents the current telemetry metadata schema.
	SchemaVersion = "2.1.0"
	// Subsystem defines the reporting service component identifier.
	Subsystem = "telemetry-worker"
)

// OperationalState captures point-in-time system heartbeat telemetry.
type OperationalState struct {
	BuildHash       string    `+"`json:\"build_hash\"`"+`
	Iteration       int64     `+"`json:\"iteration\"`"+`
	LastHeartbeat   time.Time `+"`json:\"last_heartbeat\"`"+`
	UnixTimestamp   int64     `+"`json:\"unix_timestamp\"`"+`
	RFC3339Snapshot string    `+"`json:\"rfc3339_snapshot\"`"+`
	Status          string    `+"`json:\"status\"`"+`
	ActiveWorkers   int       `+"`json:\"active_workers\"`"+`
	MemoryAllocated uint64    `+"`json:\"memory_allocated_bytes\"`"+`
	EntropySeed     string    `+"`json:\"entropy_seed\"`"+`
}

// CurrentState holds the latest telemetry heartbeat data.
var CurrentState = OperationalState{
	BuildHash:       "%s",
	Iteration:       %d,
	LastHeartbeat:   time.Unix(%d, %d),
	UnixTimestamp:   %d,
	RFC3339Snapshot: "%s",
	Status:          "HEALTHY_OPERATIONAL",
	ActiveWorkers:   %d,
	MemoryAllocated: %d,
	EntropySeed:     "%s",
}

// Snapshot returns the current operational telemetry state.
func Snapshot() OperationalState {
	return CurrentState
}
`, pkgName, buildHash, iteration, unixSec, unixNsec, unixSec, rfc3339, activeWorkers, memoryAllocated, entropySeed)

	return []byte(code), iteration
}

// PickCommitMessage selects a random Conventional Commit message.
func PickCommitMessage() string {
	idx := CryptoRandomInt(len(CommitMessages))
	return CommitMessages[idx]
}

func main() {
	logger := NewLogger()

	logger.Info("Initializing %s v%s...", AppName, AppVersion)

	cfg, err := ResolveConfig(logger)
	if err != nil {
		logger.Error("Configuration error: %v", err)
		os.Exit(1)
	}

	logger.Info("Configuration target: %s/%s on branch %q (File: %s)", cfg.Owner, cfg.Repo, cfg.Branch, cfg.TargetFile)

	// Step 1: Skip Probability Check (Anti-Bot Pattern)
	if !cfg.Now {
		roll := CryptoRandomInt(100) // 0..99
		logger.Info("Evaluating daily skip probability: %d%% configured (Dice roll: %d)", cfg.SkipProbability, roll)

		if roll < cfg.SkipProbability {
			if cfg.DryRun {
				logger.DryRun("Skip probability triggered (roll %d < %d%%). In normal execution, session would terminate now.", roll, cfg.SkipProbability)
			} else {
				logger.Info("Natural skip day triggered to simulate human schedule rest. Exiting cleanly.")
				return
			}
		}
	} else {
		logger.Info("Flag --now specified: bypassing skip probability check")
	}

	// Step 2: Initial Time Jitter Delay (Anti-Bot Pattern)
	if !cfg.Now && cfg.MaxJitterMinutes > 0 {
		// Calculate random jitter between 1 and MAX_JITTER_MINUTES
		randomMinutes := 1 + CryptoRandomInt(cfg.MaxJitterMinutes)
		randomSeconds := CryptoRandomInt(60)
		jitterDuration := time.Duration(randomMinutes)*time.Minute + time.Duration(randomSeconds)*time.Second

		if cfg.DryRun {
			logger.DryRun("Calculated initial jitter delay: %v (bypassing real wait in dry-run mode)", jitterDuration)
		} else {
			logger.Info("Waiting initial jitter delay of %v before starting commit routine...", jitterDuration)
			time.Sleep(jitterDuration)
			logger.Info("Initial jitter completed. Resuming commit sequence.")
		}
	} else if cfg.Now {
		logger.Info("Flag --now specified: bypassing initial jitter delay")
	}

	// Step 3: Determine Commit Count
	totalCommits := 1
	if cfg.ForcedCommits > 0 {
		totalCommits = cfg.ForcedCommits
		logger.Info("Commit count forced via flag: %d commit(s)", totalCommits)
	} else if cfg.Now {
		totalCommits = 1
		logger.Info("Single instant commit selected via --now flag")
	} else {
		// Random between 1 and MAX_COMMITS
		totalCommits = 1 + CryptoRandomInt(cfg.MaxCommits)
		logger.Info("Random commit count selected: %d commit(s) (1..%d)", totalCommits, cfg.MaxCommits)
	}

	client := NewGitHubClient(cfg.Token, logger)
	ctx := context.Background()

	// Step 4: Execute Commits Loop
	for i := 1; i <= totalCommits; i++ {
		logger.Info("--- Processing Commit [%d/%d] ---", i, totalCommits)

		// Always fetch current latest SHA and content before each commit to avoid 409 conflict
		var currentSHA string
		var currentContent []byte
		var fileExists bool

		if cfg.DryRun && strings.HasPrefix(cfg.Token, "ghp_dryrun_dummy") {
			logger.DryRun("Dry-run mode with placeholder token: simulating file query")
			currentSHA = "dryrun_existing_blob_sha_" + GenerateRandomHex(8)
			currentContent = []byte("// Existing mock content\n// Iteration: 10\n")
			fileExists = true
		} else {
			sha, raw, exists, getErr := client.GetFileContents(ctx, cfg.Owner, cfg.Repo, cfg.TargetFile, cfg.Branch)
			if getErr != nil {
				logger.Error("Failed to fetch current file state from GitHub: %v", getErr)
				os.Exit(1)
			}
			currentSHA = sha
			currentContent = raw
			fileExists = exists
		}

		if fileExists {
			logger.Info("Target file exists in repository. Current SHA: %s (Size: %d bytes)", currentSHA, len(currentContent))
		} else {
			logger.Info("Target file does not exist yet. Will create new file at %s", cfg.TargetFile)
		}

		// Generate updated Go payload with incremented iteration
		newPayload, iteration := GenerateCodePayload(cfg.TargetFile, string(currentContent))
		commitMsg := PickCommitMessage()

		logger.Info("Generated payload iteration #%d (%d bytes)", iteration, len(newPayload))
		logger.Info("Selected commit message: %q", commitMsg)

		if cfg.DryRun {
			logger.DryRun("SIMULATED COMMIT [%d/%d]:", i, totalCommits)
			logger.DryRun("  Action:  %s %s", func() string {
				if fileExists {
					return "UPDATE"
				}
				return "CREATE"
			}(), cfg.TargetFile)
			logger.DryRun("  Branch:  %s", cfg.Branch)
			logger.DryRun("  Message: %s", commitMsg)
			logger.DryRun("  SHA:     %s", currentSHA)
			logger.DryRun("  Payload sample:\n%s", string(newPayload))
		} else {
			// Perform actual HTTP PUT to GitHub REST API
			putResp, putErr := client.PutFileContents(ctx, cfg.Owner, cfg.Repo, cfg.TargetFile, cfg.Branch, commitMsg, newPayload, currentSHA)
			if putErr != nil {
				logger.Error("Failed to push commit to GitHub: %v", putErr)
				os.Exit(1)
			}

			logger.Success("Commit [%d/%d] successfully pushed!", i, totalCommits)
			logger.Success("  Commit SHA: %s", putResp.Commit.SHA)
			if putResp.Commit.HTMLURL != "" {
				logger.Success("  Commit URL: %s", putResp.Commit.HTMLURL)
			}
			logger.Success("  Blob SHA:   %s", putResp.Content.SHA)
		}

		// Inter-Commit Pause if there are subsequent commits remaining
		if i < totalCommits {
			// Random pause between 2 and 12 minutes (with random seconds)
			pauseMinutes := 2 + CryptoRandomInt(11) // 2..12
			pauseSeconds := CryptoRandomInt(60)
			pauseDuration := time.Duration(pauseMinutes)*time.Minute + time.Duration(pauseSeconds)*time.Second

			if cfg.DryRun {
				logger.DryRun("Simulating inter-commit pause of %v (sleeping 1 second for simulation)...", pauseDuration)
				time.Sleep(1 * time.Second)
			} else {
				logger.Info("Pausing for %v before next commit to simulate natural developer rhythm...", pauseDuration)
				time.Sleep(pauseDuration)
				logger.Info("Pause complete. Proceeding with next commit.")
			}
		}
	}

	logger.Success("All %d scheduled commit(s) successfully completed.", totalCommits)
}
