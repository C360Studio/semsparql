package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTreeStateSeesUntrackedContent: cover:check refuses a profile measured on another tree by
// comparing scripts/tree-state.sh fingerprints, so editing a new, untracked test file must change
// the fingerprint, not only adding or removing it.
func TestTreeStateSeesUntrackedContent(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "tree-state.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "tree-state.sh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	state := func() string {
		t.Helper()
		out, err := exec.Command("bash", filepath.Join(dir, "scripts", "tree-state.sh")).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	untracked := filepath.Join(dir, "new_test.go")
	if err := os.WriteFile(untracked, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := state()
	if err := os.WriteFile(untracked, []byte("package x\n\nfunc TestMore() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := state(); after == before {
		t.Fatalf("editing an untracked file left the fingerprint at %s", before)
	}
}

// TestCoverCheckRefusesAnUnmeasuredTree: with no arguments, scripts/cover-check.sh reads
// coverage/integration.coverprofile only when coverage/integration.tree holds the fingerprint
// of the tree as it is now.
func TestCoverCheckRefusesAnUnmeasuredTree(t *testing.T) {
	const refusal = "no passing integration run measured this tree"
	run := func(t *testing.T, fingerprint *string) (string, error) {
		t.Helper()
		root := copyScript(t, "cover-check.sh")
		if err := os.WriteFile(filepath.Join(root, "scripts", "tree-state.sh"), []byte("#!/bin/sh\necho 0123456789ab\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "coverage"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "coverage", "integration.coverprofile"), []byte("mode: atomic\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fingerprint != nil {
			if err := os.WriteFile(filepath.Join(root, "coverage", "integration.tree"), []byte(*fingerprint+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("bash", filepath.Join(root, "scripts", "cover-check.sh"))
		cmd.Dir = root
		cmd.Env = fakeBin(t, map[string]string{"go": "#!/bin/sh\nexit 0\n"})
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	other, same := "ba9876543210", "0123456789ab"
	for _, tc := range []struct {
		name        string
		fingerprint *string
	}{
		{"no fingerprint: the last run failed or never ran", nil},
		{"another tree's fingerprint", &other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, tc.fingerprint)
			if err == nil || !strings.Contains(out, refusal) {
				t.Fatalf("err=%v, want a refusal saying %q\n%s", err, refusal, out)
			}
		})
	}
	// The refusal is not unconditional: this tree's fingerprint gets past it. Coverage itself then
	// fails, since the fake go wrote no unit profile; only the refusal matters here.
	t.Run("this tree's fingerprint", func(t *testing.T) {
		if out, _ := run(t, &same); strings.Contains(out, refusal) {
			t.Fatalf("refused a profile measured on this tree:\n%s", out)
		}
	})
}

// TestCoverCheckThresholdAndMerging verifies 80% statement coverage enforcement across unit,
// integration, and merged profiles, including duplicate block merging and missing-statement refusal.
func TestCoverCheckThresholdAndMerging(t *testing.T) {
	const mod = "github.com/c360studio/semsparql/"
	script := filepath.Join(repoRoot(t), "scripts", "cover-check.sh")

	run := func(t *testing.T, targets, unitBody, intBody string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		unitFile := filepath.Join(dir, "unit.coverprofile")
		intFile := filepath.Join(dir, "integration.coverprofile")
		if err := os.WriteFile(unitFile, []byte(unitBody), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(intFile, []byte(intBody), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", script, unitFile, intFile)
		if targets != "" {
			cmd.Env = append(os.Environ(), "COVER_TARGETS="+targets)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	allFourEighty := "mode: atomic\n" +
		mod + "irimap/m.go:1.1,1.9 8 1\n" + mod + "irimap/m.go:2.1,2.9 2 0\n" +
		mod + "reader/r.go:1.1,1.9 8 1\n" + mod + "reader/r.go:2.1,2.9 2 0\n" +
		mod + "sparql/p.go:1.1,1.9 8 1\n" + mod + "sparql/p.go:2.1,2.9 2 0\n" +
		mod + "server/s.go:1.1,1.9 8 1\n" + mod + "server/s.go:2.1,2.9 2 0\n"
	serverSeventyNine := "mode: atomic\n" +
		mod + "irimap/m.go:1.1,1.9 8 1\n" + mod + "irimap/m.go:2.1,2.9 2 0\n" +
		mod + "reader/r.go:1.1,1.9 8 1\n" + mod + "reader/r.go:2.1,2.9 2 0\n" +
		mod + "sparql/p.go:1.1,1.9 8 1\n" + mod + "sparql/p.go:2.1,2.9 2 0\n" +
		mod + "server/s.go:1.1,1.9 79 1\n" + mod + "server/s.go:2.1,2.9 21 0\n"

	// Default target list (no COVER_TARGETS override) enforces irimap, reader, sparql, and server unit coverage.
	if out, err := run(t, "", allFourEighty, "mode: atomic\n"); err != nil || !strings.Contains(out, "cover: irimap 80.0%") || !strings.Contains(out, "cover: reader 80.0%") || !strings.Contains(out, "cover: sparql 80.0%") || !strings.Contains(out, "cover: server 80.0%") {
		t.Fatalf("default 80%% unit profile failed: err=%v\n%s", err, out)
	}
	if out, err := run(t, "", serverSeventyNine, "mode: atomic\n"); err == nil || !strings.Contains(out, "server 79.0% < 80%") {
		t.Fatalf("default 79%% unit profile did not fail: err=%v\n%s", err, out)
	}
	if out, err := run(t, "", "mode: atomic\n", allFourEighty); err == nil || !strings.Contains(out, "irimap: no statements in the unit profile") || !strings.Contains(out, "reader: no statements in the unit profile") || !strings.Contains(out, "sparql: no statements in the unit profile") || !strings.Contains(out, "server: no statements in the unit profile") {
		t.Fatalf("default missing unit statements did not fail: err=%v\n%s", err, out)
	}

	mergedUnit := "mode: atomic\n" + mod + "reader/r.go:1.1,1.9 4 1\n" + mod + "reader/r.go:2.1,2.9 4 0\n" + mod + "reader/r.go:3.1,3.9 2 0\n"
	mergedInt := "mode: atomic\n" + mod + "reader/r.go:1.1,1.9 4 0\n" + mod + "reader/r.go:2.1,2.9 4 1\n" + mod + "reader/r.go:3.1,3.9 2 0\n"
	if out, err := run(t, "reader merged", mergedUnit, mergedInt); err != nil || !strings.Contains(out, "cover: reader 80.0% (merged unit and integration profile)") {
		t.Fatalf("merged profile failed: err=%v\n%s", err, out)
	}
}
