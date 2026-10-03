package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitWorld is a three-party setup: a bare origin, a "mac" clone that commits
// and pushes through git, and the "box" work tree that Obsidian Sync writes
// into, with its git dir outside the tree like on EC2. Sync itself is
// simulated by writing files into the box tree directly.
type gitWorld struct {
	t      *testing.T
	origin string
	mac    string
	box    string // work tree
	repo   *Repo
}

func run(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newGitWorld(t *testing.T) *gitWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	w := &gitWorld{t: t, origin: filepath.Join(root, "origin.git"), mac: filepath.Join(root, "mac"), box: filepath.Join(root, "box")}
	run(t, root, nil, "init", "-q", "--bare", "-b", "master", w.origin)
	run(t, root, nil, "clone", "-q", w.origin, w.mac)
	w.macWrite("a.md", "a v1\n")
	w.macWrite("b.md", "b v1\n")
	run(t, w.mac, nil, "add", "-A")
	run(t, w.mac, nil, "commit", "-q", "-m", "init")
	run(t, w.mac, nil, "push", "-q", "-u", "origin", "master")

	// The box: Sync delivers the same files; history is attached on top as on EC2.
	gitDir := filepath.Join(root, "box-git")
	if err := os.MkdirAll(w.box, 0o755); err != nil {
		t.Fatal(err)
	}
	w.syncWrite("a.md", "a v1\n")
	w.syncWrite("b.md", "b v1\n")
	cfg := &Config{VaultDir: w.box, GitDir: gitDir, GitPush: true}
	w.repo = newRepo(cfg)
	env := []string{"GIT_DIR=" + gitDir, "GIT_WORK_TREE=" + w.box}
	run(t, w.box, env, "init", "-q", "-b", "master")
	run(t, w.box, env, "remote", "add", "origin", w.origin)
	run(t, w.box, env, "fetch", "-q", "origin")
	run(t, w.box, env, "reset", "-q", "origin/master")
	run(t, w.box, env, "branch", "-q", "--set-upstream-to=origin/master", "master")
	run(t, w.box, env, "add", "-A") // what the old bridge left in the index after its last run
	return w
}

func (w *gitWorld) macWrite(name, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.mac, name), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// macPush commits and pushes from the Mac without Sync carrying anything to the box.
func (w *gitWorld) macPush(msg string) {
	w.t.Helper()
	run(w.t, w.mac, nil, "add", "-A")
	run(w.t, w.mac, nil, "commit", "-q", "-m", msg)
	run(w.t, w.mac, nil, "push", "-q")
}

func (w *gitWorld) syncWrite(name, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.box, name), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *gitWorld) syncDelete(name string) {
	w.t.Helper()
	if err := os.Remove(filepath.Join(w.box, name)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *gitWorld) commit(label string) bool {
	w.t.Helper()
	committed, err := w.repo.CommitAndPush(context.Background(), label)
	if err != nil {
		w.t.Fatalf("CommitAndPush: %v", err)
	}
	return committed
}

// originFile returns the file at origin's tip, or "" when it does not exist.
func (w *gitWorld) originFile(name string) string {
	w.t.Helper()
	cmd := exec.Command("git", "--git-dir", w.origin, "show", "master:"+name)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func (w *gitWorld) originLog() []string {
	w.t.Helper()
	return strings.Split(run(w.t, w.origin, nil, "log", "--format=%s", "master"), "\n")
}

func TestCommitStagesClaudeEdits(t *testing.T) {
	w := newGitWorld(t)
	w.syncWrite("b.md", "b edited by claude\n")
	w.syncWrite("new.md", "created by claude\n")
	if !w.commit("edit b and add new") {
		t.Fatal("nothing committed")
	}
	if got := w.originFile("b.md"); got != "b edited by claude\n" {
		t.Errorf("b.md at origin: %q", got)
	}
	if got := w.originFile("new.md"); got != "created by claude\n" {
		t.Errorf("new.md at origin: %q", got)
	}
	if got := w.originLog()[0]; got != "assistant: edit b and add new" {
		t.Errorf("subject: %q", got)
	}
	if w.commit("again") {
		t.Error("second run with no changes committed something")
	}
}

func TestStaleCopyIsNotCommittedOverMacPush(t *testing.T) {
	w := newGitWorld(t)
	// The Mac edits a.md and pushes through git while Obsidian is closed, so
	// Sync never delivers it to the box. The box still holds a v1.
	w.macWrite("a.md", "a v2 from the mac\n")
	w.macPush("docs: a v2")

	// Claude edits b.md on the box.
	w.syncWrite("b.md", "b v2 by claude\n")
	if !w.commit("edit b") {
		t.Fatal("nothing committed")
	}
	if got := w.originFile("a.md"); got != "a v2 from the mac\n" {
		t.Fatalf("a.md was reverted by the box: %q", got)
	}
	if got := w.originFile("b.md"); got != "b v2 by claude\n" {
		t.Errorf("b.md: %q", got)
	}
	log := w.originLog()
	if log[0] != "assistant: edit b" || log[1] != "docs: a v2" {
		t.Errorf("history: %v", log)
	}

	// Sync finally delivers the Mac's a.md: the box must stay quiet about it.
	w.syncWrite("a.md", "a v2 from the mac\n")
	if w.commit("noop") {
		t.Error("delivery of already-committed content produced a commit")
	}
}

func TestMacAddedFileIsNotDeletedByBox(t *testing.T) {
	w := newGitWorld(t)
	w.macWrite("script.sh", "#!/bin/sh\n")
	w.macPush("add script")

	w.syncWrite("b.md", "b touched\n")
	if !w.commit("touch b") {
		t.Fatal("nothing committed")
	}
	if got := w.originFile("script.sh"); got != "#!/bin/sh\n" {
		t.Fatalf("script.sh deleted at origin by the box: %q", got)
	}
}

func TestSyncDeliveredMacEditsAreSwept(t *testing.T) {
	w := newGitWorld(t)
	// An edit made in Obsidian on the phone arrives through Sync between runs,
	// never through git. The next run must still carry it into history.
	w.syncWrite("a.md", "a edited on the phone\n")
	w.syncWrite("phone-note.md", "typed on the phone\n")
	if !w.commit("job morning-brief") {
		t.Fatal("nothing committed")
	}
	if got := w.originFile("a.md"); got != "a edited on the phone\n" {
		t.Errorf("a.md: %q", got)
	}
	if got := w.originFile("phone-note.md"); got != "typed on the phone\n" {
		t.Errorf("phone-note.md: %q", got)
	}
}

func TestGenuineDeletionOnBoxIsCommitted(t *testing.T) {
	w := newGitWorld(t)
	w.syncDelete("b.md")
	if !w.commit("delete b") {
		t.Fatal("nothing committed")
	}
	if got := w.originFile("b.md"); got != "" {
		t.Errorf("b.md still at origin: %q", got)
	}
}

func TestChangesSurviveFailedPush(t *testing.T) {
	w := newGitWorld(t)
	env := []string{"GIT_DIR=" + w.repo.cfg.GitDir, "GIT_WORK_TREE=" + w.box}
	run(t, w.box, env, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	w.syncWrite("b.md", "b while offline\n")
	committed, err := w.repo.CommitAndPush(context.Background(), "offline")
	if !committed || err == nil {
		t.Fatalf("want committed with push error, got committed=%v err=%v", committed, err)
	}

	run(t, w.box, env, "remote", "set-url", "origin", w.origin)
	if !w.commit("back online") {
		t.Fatal("offline change not re-committed once the remote is back")
	}
	if got := w.originFile("b.md"); got != "b while offline\n" {
		t.Errorf("b.md at origin: %q", got)
	}
}

func TestIgnoredFilesStayOut(t *testing.T) {
	w := newGitWorld(t)
	exclude := filepath.Join(w.repo.cfg.GitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte(".claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.box, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.syncWrite(".claude/settings.json", "{}\n")
	if w.commit("ignored only") {
		t.Fatal("ignored file produced a commit")
	}
}
