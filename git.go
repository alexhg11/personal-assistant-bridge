package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Repo commits the vault work tree after each Claude run. The git dir lives
// outside the vault so Claude Code never sees a repository.
type Repo struct {
	cfg *Config
}

func newRepo(cfg *Config) *Repo { return &Repo{cfg: cfg} }

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.cfg.VaultDir
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+r.cfg.GitDir,
		"GIT_WORK_TREE="+r.cfg.VaultDir,
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitAndPush stages everything, commits if anything changed, and pushes.
// A push failure is returned but the commit stays; the next push carries it.
//
// Before staging, the branch is moved to origin/master with a mixed reset so
// commits made elsewhere (the Mac) are adopted instead of fought. Only the
// index moves; the work tree stays as Obsidian Sync delivered it, which is
// the source of truth. If Sync lags, the result is a noisy pair of commits,
// never a lost file.
func (r *Repo) CommitAndPush(ctx context.Context, prompt string) (committed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if _, err := r.git(ctx, "fetch", "-q", "origin"); err != nil {
		log.Printf("git: fetch failed, committing on local history: %v", err)
	} else if _, err := r.git(ctx, "reset", "-q", "origin/master"); err != nil {
		return false, err
	}

	if _, err := r.git(ctx, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := r.git(ctx, "diff", "--cached", "--quiet"); err == nil {
		return false, nil // nothing staged
	}
	msg := "assistant: " + summarize(prompt, 60)
	if _, err := r.git(ctx, "commit", "-q", "-m", msg); err != nil {
		return false, err
	}
	if !r.cfg.GitPush {
		return true, nil
	}
	if _, err := r.git(ctx, "push", "-q"); err != nil {
		return true, err
	}
	return true, nil
}

func summarize(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
