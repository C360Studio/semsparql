# Testing in SemSparql

This page is for a developer about to write or review a test here. It covers what the test has to tell apart, which
level to run it at, how to show it can fail, and what to record in the pull request. Tasks 0.1–4.1 added `go.mod`, the
Go verification gates, `internal/harness/contract` structural guards, and all runtime packages (`irimap`, `reader`,
`sparql`, `server`, `cmd/semsparql`, including `TestScanPatternReaderParity` and `TestRunEndToEndWithLiveNATSAndGraphIngest`
against an embedded JetStream NATS server and `graph-ingest`); the dedicated `-tags=integration` DIEKM end-to-end suite
lands in task 4.2.

## Start with the wrong behavior

Before writing a test, write down two things in one sentence: the correct behavior, and one specific wrong behavior
a plausible implementation could have instead. The test is useful only if it passes for the first and fails for the
second. A test that would still pass against the wrong behavior proves nothing, however many assertions it has.

Then decide what the test observes. Choose a result a caller can see (a return value, a typed error, a response body
and status, a request sent to the reader), including side effects that must *not* happen. A successful response can
sit next to silently truncated results.

Suppose the server must stop a query at its scan budget. The wrong behavior worth catching is an off-by-one at the
budget, or a partial result returned as if complete. A graph with exactly budget-1, budget and budget+1 matching
entities tells them apart; a random graph of 1 to 10,000 entities almost never lands on the limit.

Cover both directions where they apply. A rejection test can pass against code that rejects everything, and a
round-trip test can pass against a parser that accepts invalid input consistently.

## Expected values come from somewhere else

The expected value in an assertion must come from a source independent of the code under test: the requirement, an
external specification (the SPARQL 1.1 recommendations, the W3C result formats, JSON-LD), or a small model written in
the test. If the test computes its expectation with the same algorithm as the implementation, it repeats the
implementation's mistakes and cannot fail.

The oracle (the independent source of the expected answer) for IRI mapping is `semengine/vocabulary/export` at the
pinned release: `irimap` must agree with it, so the test compares against export's output, not against a second copy
of `irimap`'s own rules. For folding, the oracle is the same query evaluated over a graph with every projection
materialized (see "Check a history against a reference model").

## Pick the lowest level that can show it

Use the cheapest level that can still expose the wrong behavior. Do not move a test up a level just because the
production code reads over NATS, and do not replace a real-broker check with a fake that cannot reproduce the behavior
being protected.

| Level | Command | What it can show |
|---|---|---|
| Unit | `task test:unit` | One function or in-process behavior, including `server` over `MemoryReader` and `NATSRPCReader` / `cmd/semsparql` over an in-process `nats-server/v2` + `graph-ingest`. Runs with `-race`. |
| Unit, repeated | `task test:repeat` | A test that depends on run order, on state left by another test, or on timing. Unit tests run several times, shuffled. |
| Integration | `task test:integration` | Behavior that runs under `-tags=integration` (full DIEKM end-to-end suite lands in task 4.2). |
| Structural guards | `task verify` | Repository-wide rules checked by `internal/harness/contract` tests and scripts (`scripts/lint-test-ports.sh`, `scripts/cleanup-roots-check.sh`). |

### Unit tests

Unit tests live in ordinary `*_test.go` files and must not start containers or bind fixed ports. During iteration,
run one package directly:

```bash
go test -race -count=1 -run '^TestBlankNodeID' ./irimap/
```

### Integration tests

Integration tests live in `*_integration_test.go` files that start with `//go:build integration`, so `go test`
without the tag does not build them. Real-broker NATS and `graph-ingest` parity tests (`TestScanPatternReaderParity` in
`reader/nats_test.go` and `TestRunEndToEndWithLiveNATSAndGraphIngest` in `cmd/semsparql/main_test.go`) start an
in-process `nats-server/v2` with JetStream on an ephemeral port (`127.0.0.1:-1`) and wire a real
`graphingest.CreateGraphIngest` component; the dedicated `-tags=integration` DIEKM end-to-end suite lands in task 4.2.

### Structural guards

A structural guard is a test or script that scans the whole repository for a rule, such as no fixed ports in tests, no
`time.Sleep` in tests, no `Skip` calls, or no import that writes to the graph. Task 0.1 added `TestAgentSetupParity`,
`TestPinnedDependencies`, `TestNoSleepsInTests`, `TestNoSkippedTests`, `TestNoHiddenTests`, `scripts/lint-test-ports.sh`,
and `scripts/cleanup-roots-check.sh`. When one is added,
it gets a paired sensitivity test: the sensitivity test plants the violation in a temporary tree and requires the guard
to name the planted file and the rule, so a guard that matches nothing, or fires for the wrong reason, fails.

## Show that the test can fail

A green test tells you nothing until you have seen it go red for the right reason. A mutation check makes one
deliberate wrong change to the code and confirms the test fails because of it. The procedure:

1. Run the selected test on the unmodified code. It passes, and it actually ran (use `-v` and check the name).
2. Make one deliberate, plausible wrong change to the implementation, the one the test is meant to catch. Leave the
   test, its inputs and its expectations untouched. The change must compile and the test must reach it.
3. Run the test again. The intended assertion fails, for the reason you expected. A timeout, a build error, or a
   different assertion failing does not count.
4. Put the original code back and run the test again. It passes.

SemSparql has no `task mutate:check` yet (semengine's lives under its `internal/`), so run the steps by hand.

### Run it by hand

Keep a copy of every file before you change it (`cp`), and record its SHA-256 checksum. Restore from that copy, and
check that the checksum matches:

```bash
cp irimap/irimap.go "${TMPDIR:-/tmp}/irimap.go.bak" && shasum -a 256 irimap/irimap.go   # before
cp "${TMPDIR:-/tmp}/irimap.go.bak" irimap/irimap.go && shasum -a 256 irimap/irimap.go   # after
```

Do not use `git checkout --`, `git restore`, `git stash` or `git reset --hard`; they can discard other work in the
tree. Do not check the restore with `git diff` either: it shows nothing for an untracked file. Record the change you
made, the commands, and the output of all three runs in the pull request.

### Outcomes

Only step 3 going red as expected counts as a detection. Record any other outcome under its own name:

- **Detection:** the baseline run passed, the run with the wrong change failed with the expected assertion, and the
  restored run passed.
- **Survivor:** the wrong change compiled and the test reached it, but the test still passed. Look for the missing
  input, assertion or scope, and report the survivor until it is resolved. If you change the test as a result, start
  again from step 1 with the new test.
- **Invalid:** the wrong change does not compile, or the test never reached it. Either way it says nothing about the
  test's assertions. Fix the change, or choose one on the path the test is meant to cover, and check again.
- **Inconclusive:** the run ended without the intended assertion failing: an error, a timeout of the whole run, a
  different or unrelated failure, or a `-run` pattern that did not select the test. A failing exit code alone is not a
  detection. Fix the cause and run the check again; never skip or weaken a test to get past it.

When the wrong change makes something never finish, the test detects it only if it waits with a time limit of its own
and fails with its own message. The timeout of the whole `go test` run shows only that the run did not finish, so the
outcome is inconclusive. A race-detector report counts as the expected failure only when you named it before running,
for a wrong change that removes a lock.

A survivor is equivalent when the wrong change alters nothing the code promises. Equivalent is never an outcome: the
outcome stays survivor, and the reviewer records the assessment (the contract the code is held to, the inputs
considered, the reasoning, who assessed it) in the pull request.

### How reach is judged

The test reaches a wrong change when it runs at least one of the changed lines. A wrong change the test never runs
cannot make it fail, so a passing run with the wrong change says nothing until reach is shown. Show it with coverage
of the unchanged code (`go test -coverprofile`) or with a temporary log line in the changed code, and say which you
used.

### Tests that generate their inputs

If the test generates its inputs, replay the same input or seed against the wrong change and against the original
code. Two different random samples differ for reasons unrelated to the change; if the same input cannot be replayed,
the outcome is inconclusive. For Rapid, fix the seed with `-rapid.seed=N` (not 0) and pass `-rapid.nofailfile` so the
wrong change leaves no failure file behind. A native fuzz target needs nothing more: plain `go test` replays only its
seeds.

### When to run one

Do this when the change:

- enforces a rule whose violation would silently return wrong or partial results, allow something forbidden (a
  write, a non-local bind without auth, a query past its budget), or leave work running after shutdown;
- emits a signal (a log line and a metric) for a skip, drop or degraded path: remove the emit, and the test must fail;
- fixes a bug that the existing tests let through; or
- has a specific wrong behavior that someone suspects the tests would miss.

If none applies, a one-line reason in the pull request is enough. If you cannot complete the experiment, say why and
what risk remains; the reviewer accepts or rejects that.

## Fuzz targets and property-based tests

Use a native Go fuzz target (`func FuzzX(f *testing.F)`) for every parser of outside input: SPARQL text, IRIs, entity
IDs, HTTP parameters. Seed it with examples of every input class it must accept and every class it must reject, and
assert a rule (it never panics, it round-trips, it agrees with an independent oracle), not a table of expected outputs.
`go test` replays only the seeds. To explore new inputs, run the fuzzer yourself:

```bash
go test -run '^$' -fuzz '^FuzzParseSPARQL$' -fuzztime 30s ./sparql/
```

In the pull request, report the two kinds of run separately: the seed replay the unit run did, and any exploratory
`-fuzz` run with its exact command, its `-fuzztime`, and what it found. A green unit run is not evidence of fuzz
exploration.

A property-based test generates many inputs or sequences of operations and checks a rule that must hold for all of
them. The rule must come from the requirement, not from reading the implementation. The library is Rapid,
`pgregory.net/rapid`, adopted from semengine; it joins `go.mod` with the first test that uses it. The properties named
in issue #1 are:

- **IRI round-trip:** for every valid entity ID, `irimap` maps it to an IRI and back to the same ID, and the IRI equals
  what `semengine/vocabulary/export` produces.
- **Folding equivalence:** a query over folded `ProjectionSpec` chains returns the same solutions as the unfolded query
  over a materialized graph (the reference model below).
- **Reader parity:** `MemoryReader` and `NATSRPCReader` answer the same calls with the same entities on the same
  graph. This one needs the integration fixture.

For both tools, the generator must be able to reach the boundary the rule is about. Being reachable is not the same as
being exercised in a given run, so keep explicit examples for any boundary you claim the test covers.

### Decide whether generated checks are needed

Before you write the tests, decide whether the change needs generated checks, and record the decision in the change's
design or the pull request. The decision is needed when the change touches any of:

- an input format with many interacting valid and invalid cases, or limits on size or count (SPARQL grammar, IRIs,
  HTTP parameters, result caps);
- a transformation with a stated law: round-trip, idempotence, normalization, equivalence of a rewrite such as folding;
- a history, where the outcome depends on the order of operations, on retry, or on shutdown.

Then either write a generated check for the rule, or name the example tests that cover it and explain why they cover
the combinations, repetitions and orderings that matter. "The existing tests pass" or a count of tests is not a
reason. There is no quota of property tests per pull request.

### Show that each assertion runs

Assertion activation is the condition under which an assertion actually executes. Check it separately from whether
the generator can reach an input. A loop over a result set that is always empty passes without checking anything; so
does an assertion behind a guard or an early return. For every rule you claim, say what makes its assertion run. If a
case must run, set it up deterministically (a fixed prefix or a named example) rather than hoping a random run hits
it. Do not require a random run to reach a rare state a minimum number of times; that makes the test flaky.

### Check a history against a reference model

A reference model is a small, simplified version of the expected behavior that the test itself owns. For folding
equivalence, the model is the generated graph with every projection written out as plain triples, queried by a naive
evaluator in the test; the real code runs the folded query over the unmaterialized graph, and the two solution
multisets must match. Never fill the model by calling the production code, and do not copy its decisions: the model is
useful only if it can disagree with a plausible bug. A sequential model does not test concurrent interleavings or
broker recovery; those need their own tests at the right level.

### What a generated run leaves behind

A zero exit, or the number of checks you asked for, does not show that the checks ran. Run with `-count=1 -v` and
read the number of checks actually completed from the tool's output. Record:

- the seed of the run, so it can be repeated (a fixed seed repeats the same inputs only for the same test code and
  library version);
- the number of checks completed;
- for a failure, the failing input and the command that replays it. Keep an important failure as a named,
  deterministic test, because a change to the generator can change what an old seed produces.

### Running and replaying a Rapid test

Rapid's flags exist only in a package that imports Rapid, so name that package rather than `./...`:

```bash
go test ./irimap -run '^TestPropIRIRoundTrip$' -count=1 -race -v -rapid.checks=100 -rapid.seed=1320
```

`-rapid.checks` sets how many cases to try (default 100) and `-rapid.seed` fixes the seed; `-rapid.seed=0` asks for a
fresh one. With `-v`, Rapid prints a summary of the checks it completed (`[rapid] OK, passed 100 tests`); that line,
not the flag, is the count to record.

The unit and repeat runs pass no Rapid flags, so each run uses a fresh seed. A rare failing input can fail one run and
pass the next on the same tree. That is a failing input Rapid found, not noise: replay it with the seed it printed
before doing anything else. It is also a known flake (`.agents/protocol.md`, "Known flakes"): file it with the
`class:flake` label and the seed; merges stop until it is fixed. The fix replays that seed, corrects the code or the
rule, and keeps the failing input as a named, deterministic test.

When a check fails, Rapid writes the shrunk case to a `.fail` file under the package's `testdata/rapid/<TestName>/`.
Replay with the printed seed or with `-rapid.failfile=<path>`. If that file no longer matches the generator, Rapid can
fall through to fresh checks, so a green run is not proof the failure was replayed; read the `-v` output. Do not commit
`.fail` files as they appear; check `git status` after a red run.

Write generators that produce only valid inputs. Rapid's `Skip`, `Skipf` and `SkipNow` discard the current case, and
tests here do not skip.

## Concurrency and cleanup

- Wait on a signal, never on `time.Sleep`. Prefer a channel, callback or `sync.WaitGroup`; next, an injected clock;
  last, bounded polling under the test's context. A longer sleep does not make an observation causal.
- Run with `-race`.
- Pass contexts in. Derive test contexts from `t.Context()` and narrow them with `context.WithTimeout`. Helpers take
  the caller's context rather than making their own.
- Bound cleanup. The test's context is already cancelled when `t.Cleanup` functions run, so cleanup needs its own
  context with a deadline: `context.WithTimeout(context.Background(), budget)`.
- Use ephemeral ports. Test the HTTP server with `httptest.NewServer` or a listener on `127.0.0.1:0`, and reach NATS
  through the fixture's URL, never a fixed address.
- Keep tests independent. A test must not depend on another test's graph, file or goroutine, and must be safe to run
  after a failure. Do not use `t.Parallel()` in tests that change process-wide state.
- Never call `t.Skip`. A test that needs a broker goes in an `//go:build integration` file instead.

## Background work

Background work is a goroutine that outlives the call that started it, such as a reader's watcher or the HTTP
server's shutdown path. It stops in one of three ways, and never by waiting a fixed time:

| Shape | Use it when |
|---|---|
| `Run(ctx) error`, preferred | The caller can run the loop on a goroutine it owns. `Run` returns when `ctx` ends, and its return is the join. |
| `Close() error`, or a stop function | The goroutine waits only on what the stop controls, such as its own ticker or done channel. `Close` cancels it and waits with no timeout. |
| `Shutdown(ctx) error` | The goroutine waits on something the stop does not control: in-flight requests, network I/O. `Shutdown` waits within `ctx` and returns `ctx.Err()` if it ends first; a later `Shutdown` returns nil. |

Two more rules apply to every shape:

- A nil context is refused at the call that receives it: with an error where the call returns one, otherwise with a
  panic at the call before any goroutine starts, never with a panic inside the goroutine.
- The code has a unit test that starts the work, uses it and stops it inside `synctest.Test`. The bubble returns only
  when every goroutine started in it has exited, so the test fails if the stop left one running. A bubble must not run
  a real nats.go connection: nats.go keeps process-wide timers that can cross bubbles. Drive fakes in the bubble, or
  give that test a process of its own.

## Budgets and failure evidence

Nothing is enforced yet. Coverage thresholds, the integration timeout and per-step timing in `task verify` arrive with
task 0.1 and later tasks; until then, state in the pull request what you ran and how long it took.

When a test waits on asynchronous or external state and fails, its message should name the condition, the elapsed
time, the attempts, the last value seen and the last error. A test that uses randomness prints its seed. A test of a
limit (timeout, result cap, scan budget) asserts the deterministic error the client sees, not just a non-nil error.

## What the pull request records

The pull request (or the change's design, linked from it) says what was expected, what was run, and what was not
covered. Fill in the first part before writing the tests and the rest after running them; leave anything not run
marked as not run.

```text
Decision:     generated check, examples enough, or not applicable; the reason says what could go wrong
Rule:         the requirement it checks, and where the expected result comes from
Inputs:       generated input or operation classes; fixed examples; what makes each assertion run
Run:          commit, exact command, seed, checks completed, result; seed replay and -fuzz exploration apart
Mutation:     the wrong change, and the baseline, wrong-change and restored runs with their outcome
Not covered:  rules not exercised, unresolved survivors, deferred checks and the reason for each
```

## What a reviewer will ask

- What wrong behavior does this test catch, and could an implementation pass it while still being wrong?
- Where does the expected value come from, and is it independent of the code under test?
- Is this the lowest level that can show the behavior?
- If the change met one of the criteria above, where are the baseline, wrong-change, and restored runs? Is any
  survivor or inconclusive run reported as such, not as a detection?
- Was the decision on generated checks recorded, with a reason that says what could go wrong, not a test count?
- Does the generator or seed corpus reach the boundary the rule is about, and what makes each assertion run?
- Does a history test compare against a model the test owns, filled from the requirement and not from the code?
- Do the seed, the number of checks completed and a replay command appear, with seed replay and fuzz exploration
  reported apart?
- Does anything wait on a sleep, use an unbounded cleanup context, skip, or bind a fixed port?
- What does the test not cover?

---

Adapted from SemEngine `docs/testing.md` at commit `67d0ee9`.
