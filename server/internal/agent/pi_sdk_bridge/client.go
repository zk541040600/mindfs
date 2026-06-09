package pisdkbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultNodeCommand = "node"
	defaultAgentDir    = "/root/.pi/agent"
	defaultTimeout     = 15 * time.Second
	maxCapturedStderr  = 2048
)

type ClientOptions struct {
	NodeCommand string
	ProbePath   string
	AgentDir    string
	Timeout     time.Duration
}

type Client struct {
	nodeCommand string
	probePath   string
	agentDir    string
	timeout     time.Duration
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return "pi sdk bridge error"
	}
	code := strings.TrimSpace(e.Code)
	msg := strings.TrimSpace(e.Message)
	if code == "" {
		return msg
	}
	if msg == "" {
		return code
	}
	return code + ": " + msg
}

type Response struct {
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type ListSessionsData struct {
	Count      int              `json:"count"`
	Returned   int              `json:"returned"`
	SessionDir string           `json:"sessionDir"`
	Sessions   []SessionSummary `json:"sessions"`
}

type ImportSessionOptions struct {
	Cwd         string
	SessionID   string
	MaxMessages int
	MaxBytes    int
}

type ImportSessionData struct {
	SessionID     string           `json:"sessionId"`
	Title         string           `json:"title"`
	MessageCount  int              `json:"messageCount"`
	ImportedCount int              `json:"importedCount"`
	SkippedCount  int              `json:"skippedCount"`
	RedactedCount int              `json:"redactedCount"`
	Truncated     bool             `json:"truncated"`
	TotalBytes    int              `json:"totalBytes"`
	Exchanges     []ImportExchange `json:"exchanges"`
	Warnings      []string         `json:"warnings"`
}

type ImportExchange struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

type SessionSummary struct {
	Path                  string         `json:"path"`
	ID                    string         `json:"id"`
	Cwd                   string         `json:"cwd"`
	Name                  string         `json:"name"`
	ParentSessionPath     string         `json:"parentSessionPath"`
	Created               string         `json:"created"`
	Modified              string         `json:"modified"`
	MessageCount          int            `json:"messageCount"`
	HasFirstMessage       bool           `json:"hasFirstMessage"`
	EntryCount            int            `json:"entryCount"`
	LeafID                string         `json:"leafId"`
	CurrentPathEntryCount int            `json:"currentPathEntryCount"`
	TreeRootCount         int            `json:"treeRootCount"`
	TreeMaxDepth          int            `json:"treeMaxDepth"`
	EntryTypeCounts       map[string]int `json:"entryTypeCounts"`
}

func NewClient(opts ClientOptions) *Client {
	nodeCommand := strings.TrimSpace(opts.NodeCommand)
	if nodeCommand == "" {
		nodeCommand = defaultNodeCommand
	}
	agentDir := strings.TrimSpace(opts.AgentDir)
	if agentDir == "" {
		agentDir = defaultAgentDir
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		nodeCommand: nodeCommand,
		probePath:   strings.TrimSpace(opts.ProbePath),
		agentDir:    agentDir,
		timeout:     timeout,
	}
}

func (c *Client) ListSessions(ctx context.Context, cwd string, limit int) (ListSessionsData, error) {
	args := []string{"--cwd", cwd, "--agent-dir", c.agentDir, "--json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	var data ListSessionsData
	if err := c.run(ctx, "list-sessions", args, &data); err != nil {
		return ListSessionsData{}, err
	}
	return data, nil
}

func (c *Client) ImportSession(ctx context.Context, opts ImportSessionOptions) (ImportSessionData, error) {
	args := []string{"--cwd", opts.Cwd, "--agent-dir", c.agentDir, "--session-id", opts.SessionID, "--json"}
	if opts.MaxMessages > 0 {
		args = append(args, "--max-messages", fmt.Sprintf("%d", opts.MaxMessages))
	}
	if opts.MaxBytes > 0 {
		args = append(args, "--max-bytes", fmt.Sprintf("%d", opts.MaxBytes))
	}
	var data ImportSessionData
	if err := c.run(ctx, "import-session", args, &data); err != nil {
		return ImportSessionData{}, err
	}
	return data, nil
}

func (c *Client) run(ctx context.Context, command string, args []string, out any) error {
	probePath, err := c.resolveProbePath()
	if err != nil {
		return err
	}
	runCtx := ctx
	cancel := func() {}
	if c.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	cmdArgs := append([]string{probePath, command}, args...)
	cmd := exec.CommandContext(runCtx, c.nodeCommand, cmdArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if runCtx.Err() != nil {
		return fmt.Errorf("pi sdk bridge %s timed out after %s", command, c.timeout)
	}

	var resp Response
	if decodeErr := json.Unmarshal(stdout.Bytes(), &resp); decodeErr != nil {
		return fmt.Errorf("pi sdk bridge %s returned invalid JSON: %w%s", command, decodeErr, stderrSuffix(stderr.String()))
	}
	if resp.Type != "response" {
		return fmt.Errorf("pi sdk bridge %s returned unexpected response type %q", command, resp.Type)
	}
	if strings.TrimSpace(resp.Command) != "" && resp.Command != command {
		return fmt.Errorf("pi sdk bridge command mismatch: requested %s got %s", command, resp.Command)
	}
	if !resp.Success {
		if resp.Error != nil {
			return resp.Error
		}
		return errors.New("pi sdk bridge command failed")
	}
	if err != nil {
		return fmt.Errorf("pi sdk bridge %s exited with error: %w%s", command, err, stderrSuffix(stderr.String()))
	}
	if out == nil {
		return nil
	}
	if len(resp.Data) == 0 {
		return errors.New("pi sdk bridge response missing data")
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("pi sdk bridge %s data decode failed: %w", command, err)
	}
	return nil
}

func (c *Client) resolveProbePath() (string, error) {
	if path := strings.TrimSpace(c.probePath); path != "" {
		return requireReadableFile(path)
	}
	candidates := []string{}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidates = append(candidates,
			filepath.Join(wd, "server", "internal", "agent", "pi_sdk_bridge", "probe.mjs"),
			filepath.Join(wd, "internal", "agent", "pi_sdk_bridge", "probe.mjs"),
		)
	}
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "server", "internal", "agent", "pi_sdk_bridge", "probe.mjs"),
			filepath.Join(dir, "..", "server", "internal", "agent", "pi_sdk_bridge", "probe.mjs"),
		)
	}
	for _, candidate := range candidates {
		if path, err := requireReadableFile(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("pi sdk bridge probe.mjs not found")
}

func requireReadableFile(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", abs)
	}
	return abs, nil
}

func stderrSuffix(stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return ""
	}
	return "; stderr=" + truncate(trimmed, maxCapturedStderr)
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
