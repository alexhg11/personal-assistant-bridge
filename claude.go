package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes Claude Code as the assistant user through the sudo-allowed
// wrapper. The wrapper owns the OAuth token and the fixed CLI flags; this
// side only passes the prompt on stdin and an optional --resume.
type Runner struct {
	cfg *Config
}

// errStaleSession is returned when --resume names a session Claude Code no
// longer has, e.g. after its transcript directory was cleared.
var errStaleSession = errors.New("stale session")

func newRunner(cfg *Config) *Runner { return &Runner{cfg: cfg} }

type claudeResult struct {
	Result    string  `json:"result"`
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
	Subtype   string  `json:"subtype"`
	Cost      float64 `json:"total_cost_usd"`
	NumTurns  int     `json:"num_turns"`
}

// Run sends prompt to Claude, resuming sessionID when non-empty. It returns
// the reply text and the session id to store for the next turn.
func (r *Runner) Run(ctx context.Context, prompt, sessionID string) (*claudeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.ClaudeTimeout)
	defer cancel()

	args := []string{"-n", "-u", r.cfg.ClaudeUser, r.cfg.ClaudeRun}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	cmd := exec.CommandContext(ctx, "sudo", args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("claude timed out after %s", r.cfg.ClaudeTimeout)
	}

	if sessionID != "" && runErr != nil && strings.Contains(stderr.String()+stdout.String(), "No conversation found with session ID") {
		return nil, errStaleSession
	}

	var res claudeResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("claude: %v: %s", runErr, firstLines(stderr.String(), 5))
		}
		return nil, fmt.Errorf("claude: unparseable output: %s", firstLines(stdout.String(), 5))
	}
	if res.IsError && res.Result == "" {
		res.Result = "Claude returned an error (" + res.Subtype + ")."
	}
	if runErr != nil && res.Result == "" {
		return nil, fmt.Errorf("claude: %v: %s", runErr, firstLines(stderr.String(), 5))
	}
	return &res, nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
