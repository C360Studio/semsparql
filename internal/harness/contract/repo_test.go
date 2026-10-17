package contract

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoRoot walks up from the package directory to the module root. Tests run with the package
// directory as the working directory, so the first go.mod above it is SemSparql's.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the contract package")
		}
		dir = parent
	}
}

// repoFiles lists tracked files plus untracked files git does not ignore, relative to root. Using
// git's view rather than a directory walk keeps node_modules, .worktrees, and coverage output out
// while still catching a new file before it is added.
func repoFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	seen := map[string]bool{}
	for _, f := range bytes.Split(out, []byte{0}) {
		name := string(f)
		if name == "" || seen[name] {
			continue
		}
		// A tracked file deleted in the working tree is not content to check.
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			continue
		}
		seen[name] = true
		files = append(files, filepath.ToSlash(name))
	}
	sort.Strings(files)
	return files
}

// writeTree creates files under a fresh temporary root for sensitivity tests and returns the root
// and the sorted relative paths.
func writeTree(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var names []string
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return root, names
}

// readLines returns a file's lines; the caller reports line numbers as index+1.
func readLines(t *testing.T, root, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", name, err)
	}
	return lines
}

// requireViolation fails unless some violation mentions every fragment. Sensitivity tests use it
// so a check that fires for the wrong reason does not count as detecting the seeded defect.
func requireViolation(t *testing.T, violations []string, fragments ...string) {
	t.Helper()
	for _, v := range violations {
		all := true
		for _, f := range fragments {
			if !strings.Contains(v, f) {
				all = false
				break
			}
		}
		if all {
			return
		}
	}
	t.Fatalf("no violation mentions %q; got %d violation(s):\n  %s",
		fragments, len(violations), strings.Join(violations, "\n  "))
}

func requireNoViolations(t *testing.T, what string, violations []string) {
	t.Helper()
	if len(violations) > 0 {
		t.Fatalf("%s violations:\n  %s", what, strings.Join(violations, "\n  "))
	}
}

// fakeBin writes executable scripts named by tools into a fresh directory and returns an
// environment whose PATH starts with it, so a script under test runs the fakes instead of the
// real tools. Tests here never invoke the real go or gh.
func fakeBin(t *testing.T, tools map[string]string, extra ...string) []string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			kv = "PATH=" + dir + ":" + strings.TrimPrefix(kv, "PATH=")
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// copyScript copies scripts/<name> from the repository into scripts/ under a fresh throwaway
// root and returns that root.
func copyScript(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", name), src, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
