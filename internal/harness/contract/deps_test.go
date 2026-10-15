package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c360studio/semengine/pkg/types"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semvocab/packs"
	"github.com/c360studio/semvocab/packs/diekm"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const (
	wantModulePath       = "github.com/c360studio/semsparql"
	semengineModulePath  = "github.com/c360studio/semengine"
	wantSemengineVersion = "v0.1.0-alpha.2"
	semvocabModulePath   = "github.com/c360studio/semvocab"
	wantSemvocabVersion  = "v0.1.0"
)

// TestPinnedDependencies enforces AGENTS.md §1 ("SemEngine and SemVocab are pinned to released
// tags: no replace, fork, local copy, or pseudo-version") on go.mod.
func TestPinnedDependencies(t *testing.T) {
	requireNoViolations(t, "pinned dependencies", pinnedDependencyViolations(t, repoRoot(t)))
}

func TestPinnedDependenciesSensitivity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	base := string(raw)

	root, _ := writeTree(t, map[string]string{"go.mod": base})
	requireNoViolations(t, "clean go.mod fixture", pinnedDependencyViolations(t, root))

	for _, tc := range []struct {
		name  string
		goMod string
		wants []string
	}{
		{
			name:  "wrong module path",
			goMod: strings.Replace(base, "module "+wantModulePath, "module github.com/c360studio/other", 1),
			wants: []string{"go.mod: module", wantModulePath},
		},
		{
			name:  "missing semengine pin",
			goMod: strings.Replace(base, "\t"+semengineModulePath+" "+wantSemengineVersion+"\n", "", 1),
			wants: []string{"go.mod", semengineModulePath, "missing direct require"},
		},
		{
			name:  "missing semvocab pin",
			goMod: strings.Replace(base, "\t"+semvocabModulePath+" "+wantSemvocabVersion+"\n", "", 1),
			wants: []string{"go.mod", semvocabModulePath, "missing direct require"},
		},
		{
			name:  "pseudo-version on semengine",
			goMod: strings.Replace(base, semengineModulePath+" "+wantSemengineVersion, semengineModulePath+" v0.1.0-alpha.2.0.20261015182956-11da4721c223", 1),
			wants: []string{"go.mod", semengineModulePath, "pseudo-version"},
		},
		{
			name:  "pseudo-version on semvocab",
			goMod: strings.Replace(base, semvocabModulePath+" "+wantSemvocabVersion, semvocabModulePath+" v0.0.0-20261015210132-64d3b8bc789c", 1),
			wants: []string{"go.mod", semvocabModulePath, "pseudo-version"},
		},
		{
			name:  "replace directive present",
			goMod: base + "\nreplace github.com/c360studio/semvocab => ../semvocab\n",
			wants: []string{"go.mod", "replace directive", semvocabModulePath},
		},
		{
			name:  "exclude directive present",
			goMod: base + "\nexclude github.com/c360studio/semengine v0.1.0-alpha.1\n",
			wants: []string{"go.mod", "exclude directive", semengineModulePath},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := writeTree(t, map[string]string{"go.mod": tc.goMod})
			requireViolation(t, pinnedDependencyViolations(t, dir), tc.wants...)
		})
	}
}

func pinnedDependencyViolations(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("go.mod: %v", err)}
	}
	mf, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return []string{fmt.Sprintf("go.mod: parse: %v", err)}
	}

	var out []string
	if mf.Module == nil || mf.Module.Mod.Path != wantModulePath {
		got := "<nil>"
		if mf.Module != nil {
			got = mf.Module.Mod.Path
		}
		out = append(out, fmt.Sprintf("go.mod: module = %q, want %q", got, wantModulePath))
	}
	for _, r := range mf.Replace {
		out = append(out, fmt.Sprintf("go.mod: replace directive %s => %s is forbidden (upstream boundary rule)", r.Old.Path, r.New.Path))
	}
	for _, ex := range mf.Exclude {
		out = append(out, fmt.Sprintf("go.mod: exclude directive %s@%s is forbidden", ex.Mod.Path, ex.Mod.Version))
	}

	direct := map[string]string{}
	for _, req := range mf.Require {
		if !req.Indirect {
			direct[req.Mod.Path] = req.Mod.Version
		}
	}
	for _, want := range []struct{ mod, ver string }{
		{semengineModulePath, wantSemengineVersion},
		{semvocabModulePath, wantSemvocabVersion},
	} {
		got, ok := direct[want.mod]
		if !ok {
			out = append(out, fmt.Sprintf("go.mod: %s: missing direct require (want %s)", want.mod, want.ver))
			continue
		}
		if module.IsPseudoVersion(got) {
			out = append(out, fmt.Sprintf("go.mod: %s pinned to pseudo-version %q; must use released tag %q", want.mod, got, want.ver))
		}
		if got != want.ver {
			out = append(out, fmt.Sprintf("go.mod: %s = %q, want %q", want.mod, got, want.ver))
		}
	}
	return out
}

// TestPinnedUpstreamSeamsSmoke verifies that the pinned SemEngine (v0.1.0-alpha.2) and SemVocab
// (v0.1.0) packages expose the exact seams recorded in the bootstrap inventory without mutating
// process-global registries, and keeps both modules referenced so `go mod tidy -diff` retains
// their direct requirements before task 1.1 lands.
func TestPinnedUpstreamSeamsSmoke(t *testing.T) {
	bfoSpec, err := packs.BFOCCOCoreSpec()
	if err != nil {
		t.Fatalf("packs.BFOCCOCoreSpec(): %v", err)
	}
	if bfoSpec.Name != "bfo-cco-core" {
		t.Fatalf("bfo-cco-core pack name = %q, want %q", bfoSpec.Name, "bfo-cco-core")
	}

	diekmSpec, err := diekm.Spec()
	if err != nil {
		t.Fatalf("diekm.Spec(): %v", err)
	}
	if diekmSpec.Name != "diekm" || len(diekmSpec.EntityTypes) != 2 || len(diekmSpec.Predicates) != 2 {
		t.Fatalf("diekm.Spec() = {name:%q, entity_types:%d, predicates:%d}, want {name:\"diekm\", entity_types:2, predicates:2}",
			diekmSpec.Name, len(diekmSpec.EntityTypes), len(diekmSpec.Predicates))
	}
	if len(diekm.Shapes()) == 0 {
		t.Fatal("diekm.Shapes() returned empty SHACL shapes")
	}

	const sampleID = "c360.ops.uas1.intel.platform.alpha"
	if err := types.ValidateEntityIDPattern("c360.ops.*.intel.platform.*"); err != nil {
		t.Fatalf("ValidateEntityIDPattern: %v", err)
	}
	matched, err := types.MatchEntityIDPattern("c360.ops.*.intel.platform.*", sampleID)
	if err != nil || !matched {
		t.Fatalf("MatchEntityIDPattern(%q) = (%v, %v), want (true, nil)", sampleID, matched, err)
	}

	gotIRI := export.SubjectIRI(sampleID, "https://example.org")
	const wantIRI = "https://example.org/entities/c360/ops/uas1/intel/platform/alpha"
	if gotIRI != wantIRI {
		t.Fatalf("export.SubjectIRI(%q) = %q, want %q", sampleID, gotIRI, wantIRI)
	}
}
