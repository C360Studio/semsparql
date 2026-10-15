package contract

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Split so this file does not contain the shape the guard refuses.
const (
	background = "context." + "Background()"
	todo       = "context." + "TODO()"
)

// The bounded-cleanup guard is a script so `task verify` runs it as its own step; this test proves
// it fires on unbounded cleanup roots and stays quiet on the bounded form, in a throwaway repository.
func TestCleanupRootsCheckSensitivity(t *testing.T) {
	script := filepath.Join(repoRoot(t), "scripts", "cleanup-roots-check.sh")
	run := func(t *testing.T, files map[string]string) (string, error) {
		t.Helper()
		root, _ := writeTree(t, files)
		if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		out, err := exec.Command("bash", script, root).CombinedOutput()
		return string(out), err
	}

	bounded := "package x\n\nfunc TestX(t *testing.T) {\n" +
		"\tctx, cancel := context.WithTimeout(context.Background(), time.Second)\n\tdefer cancel()\n\tdefer o.Stop(ctx)\n}\n"
	if out, err := run(t, map[string]string{"x/x_test.go": bounded,
		"x/x.go": "package x\n\nfunc f() { o.Stop(" + background + ") }\n"}); err != nil {
		t.Fatalf("bounded cleanup and non-test code were refused: %v\n%s", err, out)
	}

	for _, seeded := range []string{
		"\tdefer o.Stop(" + background + ")",
		"\tt.Cleanup(func() { _ = conn.Close(" + todo + ") })",
		"\trequire(c.Terminate(" + background + "))",
	} {
		out, err := run(t, map[string]string{"x/x_test.go": "package x\n\nfunc TestX(t *testing.T) {\n" + seeded + "\n}\n"})
		if err == nil || !strings.Contains(out, "x/x_test.go:4:") {
			t.Errorf("seeded %q: err=%v, want exit 1 naming x/x_test.go:4\n%s", seeded, err, out)
		}
	}
}
