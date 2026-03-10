package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildCommitMessageEmpty(t *testing.T) {
	got := buildCommitMessage(nil)
	if got != "auto: wip" {
		t.Errorf("expected 'auto: wip', got %q", got)
	}
	got = buildCommitMessage([]string{})
	if got != "auto: wip" {
		t.Errorf("expected 'auto: wip' for empty slice, got %q", got)
	}
}

func TestBuildCommitMessageFewFiles(t *testing.T) {
	files := []string{"main.go", "README.md"}
	got := buildCommitMessage(files)
	want := "auto: main.go, README.md"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBuildCommitMessageExactlyFive(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
	got := buildCommitMessage(files)
	want := "auto: a.go, b.go, c.go, d.go, e.go"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBuildCommitMessageMoreThanFive(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go"}
	got := buildCommitMessage(files)
	if !strings.HasPrefix(got, "auto: a.go, b.go, c.go, d.go, e.go") {
		t.Errorf("expected prefix 'auto: a.go, ...', got %q", got)
	}
	if !strings.Contains(got, "2 more") {
		t.Errorf("expected '2 more' in message, got %q", got)
	}
}

func TestRunGitDryRun(t *testing.T) {
	origDryRun := dryRun
	dryRun = true
	defer func() { dryRun = origDryRun }()

	var buf bytes.Buffer
	origStdout := runGitOutput
	runGitOutput = &buf
	defer func() { runGitOutput = origStdout }()

	err := runGit("/tmp", "add", ".")
	if err != nil {
		t.Fatalf("expected no error in dry-run, got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got %q", out)
	}
	if !strings.Contains(out, "add") {
		t.Errorf("expected 'add' in dry-run output, got %q", out)
	}
}
