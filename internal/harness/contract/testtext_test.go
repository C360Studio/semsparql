package contract

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Split so this file does not contain the literal it forbids.
var sleepLiteral = "time." + "Sleep"

func TestNoSleepsInTests(t *testing.T) {
	root := repoRoot(t)
	requireNoViolations(t, "sleep in tests", sleepViolations(t, root, repoFiles(t, root)))
}

func TestNoSleepsInTestsSensitivity(t *testing.T) {
	clean := map[string]string{
		"x/x_test.go": "package x\n\nvar d = time.Millisecond\n",
		// Production code outside the harness may wait; the rule is for tests and harness code.
		"x/x.go": "package x\n\nfunc f() { " + sleepLiteral + "(1) }\n",
	}
	root, files := writeTree(t, clean)
	requireNoViolations(t, "clean fixture", sleepViolations(t, root, files))

	for _, tc := range []struct {
		name  string
		tree  map[string]string
		wants []string
	}{
		{"sleep in a test file", map[string]string{
			"x/x_test.go": "package x\n\nfunc TestX(t *testing.T) {\n\t" + sleepLiteral + "(10 * time.Millisecond)\n}\n",
		}, []string{"x/x_test.go:4", sleepLiteral}},
		{"sleep in harness code", map[string]string{
			"x/x_test.go":                "package x\n",
			"internal/harness/h/wait.go": "package h\n\nfunc wait() {\n\t" + sleepLiteral + "(time.Second)\n}\n",
		}, []string{"internal/harness/h/wait.go:4", sleepLiteral}},
		{"no test file", map[string]string{"x/x.go": "package x\n"}, []string{"scanned no test file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, files := writeTree(t, tc.tree)
			requireViolation(t, sleepViolations(t, root, files), tc.wants...)
		})
	}
}

// sleepViolations scans every *_test.go file and every Go file under internal/harness/.
func sleepViolations(t *testing.T, root string, files []string) []string {
	t.Helper()
	inScope := func(name string) bool {
		return strings.HasSuffix(name, ".go") && strings.HasPrefix(name, "internal/harness/")
	}
	return scanTestText(t, root, files, inScope, func(line string) string {
		if strings.Contains(line, sleepLiteral) {
			return "`" + sleepLiteral + "`; wait on a channel, callback or synctest bubble instead"
		}
		return ""
	})
}

// scanTestText applies match to every line of every *_test.go file and of every other file
// alsoScan accepts, and reports each non-empty result as file:line. A scan that read no test file
// is itself a violation: a check that matched nothing because it saw nothing is not a pass.
func scanTestText(t *testing.T, root string, files []string, alsoScan func(string) bool, match func(string) string) []string {
	t.Helper()
	var violations []string
	tests := 0
	for _, name := range files {
		isTest := strings.HasSuffix(name, "_test.go")
		if isTest {
			tests++
		}
		if !isTest && (alsoScan == nil || !alsoScan(name)) {
			continue
		}
		for i, line := range readLines(t, root, name) {
			if why := match(line); why != "" {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", name, i+1, why))
			}
		}
	}
	if tests == 0 {
		violations = append(violations, fmt.Sprintf("scanned no test file among %d file(s)", len(files)))
	}
	return violations
}

// No *_test.go file calls Skip, Skipf or SkipNow, and none carries a build constraint other than
// integration. The pattern is built so this file does not match it.
var skipCall = regexp.MustCompile(`\.(` + "Skip|Skipf|SkipNow" + `)\(`)

func TestNoSkippedTests(t *testing.T) {
	root := repoRoot(t)
	requireNoViolations(t, "skip", skipViolations(t, root, repoFiles(t, root)))
}

func TestNoSkippedTestsSensitivity(t *testing.T) {
	root, files := writeTree(t, map[string]string{
		"x/x_test.go": "package x\n\n// Skip is a word in a comment, not a call.\nfunc TestX(t *testing.T) {}\n",
		"x/x.go":      "package x\n\nfunc f(t T) { t." + "Skip(\"production code is not a test\") }\n",
	})
	requireNoViolations(t, "clean fixture", skipViolations(t, root, files))

	for _, call := range []string{"Skip(\"flaky\")", "Skipf(\"pid %d\", 1)", "SkipNow()"} {
		t.Run(call, func(t *testing.T) {
			root, files := writeTree(t, map[string]string{
				"x/x_test.go": "package x\n\nfunc TestX(t *testing.T) {\n\tt." + call + "\n}\n",
			})
			requireViolation(t, skipViolations(t, root, files), "x/x_test.go:4", call[:strings.Index(call, "(")])
		})
	}
	t.Run("no test file", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{"x/x.go": "package x\n"})
		requireViolation(t, skipViolations(t, root, files), "scanned no test file")
	})
}

func skipViolations(t *testing.T, root string, files []string) []string {
	t.Helper()
	return scanTestText(t, root, files, nil, func(line string) string {
		if m := skipCall.FindStringSubmatch(line); m != nil {
			return "skip call `" + m[1] + "`; a test that cannot run fails with its reason"
		}
		return ""
	})
}

func TestNoHiddenTests(t *testing.T) {
	root := repoRoot(t)
	requireNoViolations(t, "build constraint", buildTagViolations(t, root, repoFiles(t, root)))
}

func TestNoHiddenTestsSensitivity(t *testing.T) {
	moved := map[string]string{
		"x/x_integration_test.go": "//go:build integration\n\npackage x\n\nfunc TestNeedsBroker(t *testing.T) {}\n",
	}
	root, files := writeTree(t, moved)
	requireNoViolations(t, "integration-tagged fixture", buildTagViolations(t, root, files))
	requireNoViolations(t, "integration-tagged fixture", skipViolations(t, root, files))

	for _, tc := range []struct{ name, content, want string }{
		{"other tag", "//go:build flaky\n\npackage x\n", "//go:build flaky"},
		{"integration combined", "//go:build integration && linux\n\npackage x\n", "//go:build integration && linux"},
		{"legacy form", "// +build flaky\n\npackage x\n", "// +build flaky"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, files := writeTree(t, map[string]string{"x/x_test.go": tc.content})
			requireViolation(t, buildTagViolations(t, root, files), "x/x_test.go:1", tc.want)
		})
	}
	t.Run("no test file", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{"x/x.go": "//go:build flaky\n\npackage x\n"})
		requireViolation(t, buildTagViolations(t, root, files), "scanned no test file")
	})
}

// buildTagViolations reads each test file's header, the lines before its package clause, where Go
// accepts build constraints.
func buildTagViolations(t *testing.T, root string, files []string) []string {
	t.Helper()
	var violations []string
	tests := 0
	for _, name := range files {
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		tests++
		for i, line := range readLines(t, root, name) {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "package ") {
				break
			}
			if (strings.HasPrefix(trimmed, "//go:build") && trimmed != "//go:build integration") || strings.HasPrefix(trimmed, "// +build") {
				violations = append(violations, fmt.Sprintf("%s:%d: build constraint `%s`; only `//go:build integration` may gate a test", name, i+1, trimmed))
			}
		}
	}
	if tests == 0 {
		violations = append(violations, fmt.Sprintf("scanned no test file among %d file(s)", len(files)))
	}
	return violations
}
