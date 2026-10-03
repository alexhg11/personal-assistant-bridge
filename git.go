package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	return r.gitIn(ctx, "", nil, args...)
}

// gitIn runs git against the vault; indexFile, when set, replaces the index
// (GIT_INDEX_FILE) and stdin feeds the command.
func (r *Repo) gitIn(ctx context.Context, indexFile string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.cfg.VaultDir
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+r.cfg.GitDir,
		"GIT_WORK_TREE="+r.cfg.VaultDir,
		"GIT_TERMINAL_PROMPT=0",
	)
	if indexFile != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+indexFile)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitAndPush commits what changed in the work tree since the box's last
// successful commit, on top of origin/master, and pushes.
//
// The vault has two writers: this box (Claude edits, plus whatever Obsidian
// Sync delivers from the Mac and the phone) and the Mac, which commits
// through git directly. Sync and git are independent, so the box's work tree
// can lag behind origin/master: a note the Mac edited and pushed while
// Obsidian was closed still has its old content here. Staging every
// difference against origin/master would commit that old copy over the Mac's
// change and look like a revert.
//
// So the box keeps its own memory of the tree it last committed, a second
// index file next to the git dir ("seen"). Each run stages only the paths
// whose work-tree content differs from that memory: Claude's edits, and
// anything Sync delivered since. A path that is unchanged on the box, however
// different it is from origin/master, is left alone. The branch is moved to
// origin/master with a mixed reset first so commits made on the Mac are
// adopted rather than fought; the work tree is never touched.
//
// The memory advances only after a successful push (or commit, when pushing
// is off), so a failed push leaves the changes to be staged again next run.
func (r *Repo) CommitAndPush(ctx context.Context, prompt string) (committed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	seen := filepath.Join(r.cfg.GitDir, "box.index")
	next := seen + ".next"
	if err := r.ensureSeen(ctx, seen); err != nil {
		return false, err
	}
	if err := copyFile(seen, next); err != nil {
		return false, err
	}
	defer os.Remove(next)

	seenTree, err := r.gitIn(ctx, seen, nil, "write-tree")
	if err != nil {
		return false, err
	}
	if _, err := r.gitIn(ctx, next, nil, "add", "-A"); err != nil {
		return false, err
	}
	out, err := r.gitIn(ctx, next, nil, "diff", "--cached", "--name-only", "--no-renames", "-z", seenTree)
	if err != nil {
		return false, err
	}
	changed := splitNUL(out)
	if len(changed) == 0 {
		return false, nil
	}

	if _, err := r.git(ctx, "fetch", "-q", "origin"); err != nil {
		log.Printf("git: fetch failed, committing on local history: %v", err)
	} else if _, err := r.git(ctx, "reset", "-q", "origin/master"); err != nil {
		return false, err
	}

	if _, err := r.gitIn(ctx, "", joinNUL(changed), "update-index", "--add", "--remove", "-z", "--stdin"); err != nil {
		return false, err
	}
	if _, err := r.git(ctx, "diff", "--cached", "--quiet"); err == nil {
		// Everything that changed here already matches origin/master, e.g.
		// Sync delivered a note the Mac had committed. Remember it, no commit.
		return false, os.Rename(next, seen)
	}
	if n := r.leftAlone(ctx); n > 0 {
		log.Printf("git: %d path(s) differ from origin/master but did not change on this box; left alone", n)
	}
	msg := "assistant: " + summarize(prompt, 60)
	if _, err := r.git(ctx, "commit", "-q", "-m", msg); err != nil {
		return false, err
	}
	if r.cfg.GitPush {
		if _, err := r.git(ctx, "push", "-q"); err != nil {
			return true, err
		}
	}
	return true, os.Rename(next, seen)
}

// ensureSeen bootstraps the box's memory on first use. The main index is
// what the previous bridge left after its last `git add -A`, i.e. the work
// tree as of its last commit, so it is the right starting point; without one,
// HEAD's tree is used, which makes the first run stage every difference like
// the old behaviour did once.
func (r *Repo) ensureSeen(ctx context.Context, seen string) error {
	if _, err := os.Stat(seen); err == nil {
		return nil
	}
	if main := filepath.Join(r.cfg.GitDir, "index"); fileExists(main) {
		log.Printf("git: bootstrapping %s from the index", filepath.Base(seen))
		return copyFile(main, seen)
	}
	log.Printf("git: bootstrapping %s from HEAD", filepath.Base(seen))
	if _, err := r.gitIn(ctx, seen, nil, "read-tree", "HEAD"); err != nil {
		_, err = r.gitIn(ctx, seen, nil, "read-tree", "--empty")
		return err
	}
	return nil
}

// leftAlone counts work-tree paths that still differ from the index after
// staging: the lag between Sync and git, reported so a revert-looking
// situation is visible in the log instead of in history.
func (r *Repo) leftAlone(ctx context.Context) int {
	out, err := r.git(ctx, "status", "--porcelain", "--untracked-files=all", "-z")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range splitNUL(out) {
		if len(e) >= 3 && (e[1] != ' ' || e[0] == '?') {
			n++
		}
	}
	return n
}

func splitNUL(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinNUL(paths []string) []byte {
	var b bytes.Buffer
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func summarize(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
