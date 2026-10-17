# SemSparql Agent Profiles

The files in `contracts/` are the tracked, platform-neutral behavioral authority. Platform adapters are intentionally
thin and must point to exactly one canonical contract. Adopted from SemEngine at commit `67d0ee9` (itself adapted from
SemStreams at `5457b345`); where a rule differs, this tree is authoritative.

## Role and platform mapping

- SemSparql architect (read-only)
  - Canonical: `.agents/contracts/semsparql-architect.md`
  - Claude: `.claude/agents/semsparql-architect.md`
  - Codex: `.codex/agents/semsparql-architect.toml`
  - Gemini (Antigravity): `define_subagent` (`name = "semsparql-architect"`, `enable_write_tools = false`)
- SemSparql developer
  - Canonical: `.agents/contracts/semsparql-developer.md`
  - Claude: `.claude/agents/semsparql-developer.md`
  - Codex: `.codex/agents/semsparql-developer.toml`
  - Gemini (Antigravity): `define_subagent` (`name = "semsparql-developer"`, `enable_write_tools = true`)
- SemSparql reviewer (independent; read-only)
  - Canonical: `.agents/contracts/semsparql-reviewer.md`
  - Claude: `.claude/agents/semsparql-reviewer.md`
  - Codex: `.codex/agents/semsparql-reviewer.toml`
  - Gemini (Antigravity): `define_subagent` (`name = "semsparql-reviewer"`, `enable_write_tools = false`)
- SemSparql technical writer
  - Canonical: `.agents/contracts/semsparql-technical-writer.md`
  - Claude: `.claude/agents/semsparql-technical-writer.md`
  - Codex: `.codex/agents/semsparql-technical-writer.toml`
  - Gemini (Antigravity): `define_subagent` (`name = "semsparql-technical-writer"`, `enable_write_tools = true`)

SemStreams also has explorer and judge contracts; SemEngine left them out, and so does SemSparql. Add one only with a
recorded owner decision.

## Model routing

Adapters separate **model tier** from **reasoning effort** (`low -> medium -> high`). Neither the architect nor the
reviewer runs on a weaker model tier than the developer.

| Role | Claude | Codex | Gemini (Antigravity) | Target effort |
| --- | --- | --- | --- | --- |
| Architect | `opus` | `gpt-6-astra`, `medium` | `inherit` or `pro` (`enable_write_tools = false`) | `medium` default; escalate to `high` for cross-cutting concurrency/boundary designs |
| Developer | `opus` | `gpt-6-sol`, `medium` | `inherit` or `pro` (`enable_write_tools = true`) | `medium` default for bounded work; escalate to `high` for complex/failed-review rounds |
| Reviewer | `opus` | `gpt-6-astra`, `high` | `inherit` or `pro` (`enable_write_tools = false`) | `high` (single-pass audit of record against review-only rules) |
| Technical writer | `sonnet` | `gpt-6-sol`, `medium` | `flash` or `inherit` (`enable_write_tools = true`) | `low` or `medium` (mechanical doc and task-truth sync) |

- **Model tier:** `semsparql-architect`, `semsparql-developer`, and `semsparql-reviewer` run on the frontier model
  (`opus` on Claude, `gpt-6-astra` / `gpt-6-sol` on Codex, `inherit` / `pro` on Gemini). `semsparql-technical-writer`
  is non-decision-making (doc sync, `task docs:check`, and conservative task ticking) and runs on a lighter tier where
  supported (`sonnet` on Claude, `flash` on Gemini).
- **Reasoning effort (`low -> medium -> high`):** Start `semsparql-architect` and `semsparql-developer` at `medium`
  effort by default and escalate to `high` only when a change crosses module boundaries, designs a concurrency or
  durable-execution primitive, or fails a review round. Keep `semsparql-reviewer` at `high` effort because it runs once
  per ready diff as the independent audit against the repository's review-only rules. Run `semsparql-technical-writer`
  at `low` or `medium` effort. The tracked `.claude/agents/` and `.codex/agents/` adapter files pin these defaults,
  and `TestAgentSetupParity` (`internal/harness/contract/agentsetup_test.go`) enforces them in `task test:unit`.

On Gemini (Antigravity), role subagents are registered with `define_subagent` pointing at the matching
`.agents/contracts/semsparql-*.md` contract and invoked with `invoke_subagent`; `Model` defaults to `"inherit"` (for
example, Gemini 4 Argon High) unless `"pro"` or `"flash"` is explicitly requested.

The table does not rank one platform's models against another's; across agents the pairing is the owner's decision.
For a code pull request the reviewer of record is another agent's reviewer from the table above; a documents-only
pull request keeps this repository's reviewer (adopted from SemEngine: `.agents/protocol.md`, "Cross-agent review").
`scripts/merge-check.sh` recognizes `claude`, `codex`, and `gemini` on code pull requests.

## Orchestrating role agents

These rules bind the session that intakes owner requests and spawns/briefs role agents. They come from SemEngine's
record of where an orchestrating session's cost went: every review round found something real, so the review loop is
not where to cut, and the avoidable cost was in the briefs and unclarified scope.

- **Clarify Definition of Done before starting or delegating work.** Before mutating state or spawning a role agent on
  an underspecified prompt, do a quick read-only check of any named issue, PR, or file (never asking what the tree
  already answers), then verify four boundaries are clear: (1) **Deliverable** (answer/audit, issue, OpenSpec design,
  docs-only PR, or code PR), (2) **Scope and non-goals** (in-bounds packages/files and what must not change),
  (3) **Definition of Done and verification** (observable completion criteria and `task` gates), and (4) **Stop
  boundary** (pause after inventory/design for an owner decision vs run through to a ready PR). If any of the four is
  missing or ambiguous, stop, propose a concise 3–4 bullet recommended Definition of Done, and ask at most 1–3 targeted
  questions; once confirmed, run autonomously to that finish line without stopping for routine steps.
- **A brief carries the owner's intent, not only the approved document.** State what the product is for
  (`AGENTS.md`, "What this is for") and any owner statement that bears on the question. In SemEngine, an architect
  briefed with a plan's heuristic alone drew a boundary that left out a capability the owner wanted, and correcting it
  took another inventory pass and its review.
- **An orchestrator's paraphrase is an unreviewed claim.** Hand an agent the artifact itself (a path, an issue
  comment URL, a `file:line`), not a summary of it. Where a summary is unavoidable, mark it as the orchestrator's so
  the reviewer checks it like any other claim.
- **Resume for continuity, start fresh for mechanics.** A resumed agent re-reads its own transcript on every resume.
  For a mechanical follow-up, a fresh agent with a precise brief costs less.
- **A design gets three review rounds.** Findings still open after the third go to the owner on the issue, with the
  choice stated, instead of a fourth round. This does not cut review short; it moves the choice to the person who
  can make it. In SemEngine one design took at least eleven rounds before any code. The architect contract's rules on
  library probes, open pull requests and behaviour-not-mechanism remove the causes of most of them.
- **Scope an inventory before it starts.** An inventory names its question and the repositories it reads first:
  this one, plus SemEngine and SemVocab at their pinned tags for the surfaces SemSparql consumes.

## Shared work protocol and rule index

`.agents/protocol.md` is the canonical shared work protocol (state homes, rituals, worktree hygiene). `CLAUDE.md` and
`AGENTS.md` point to it and carry the four gates (clarify, claim, merge, close) inline; edit the protocol only in
`.agents/protocol.md`. `.agents/rules-index.md` is the full test-by-test enforcement audit matrix backing the
domain-grouped rule summary in `AGENTS.md`.

## Shared skills

Canonical skills live in `skills/`. Read the relevant `SKILL.md` fully; the platform adapters add discovery
metadata and argument handling, not another rule set. Repository protocol and role contracts remain authoritative.

- [semsparql-preflight](skills/semsparql-preflight/SKILL.md): scope existing verification gates and record their
  evidence. Claude command `/preflight`.
- [semsparql-handoff](skills/semsparql-handoff/SKILL.md): publish shared state and preserve unfinished work. Claude
  command `/semsparql-handoff`.
- [semsparql-pickup](skills/semsparql-pickup/SKILL.md): reconcile current state and verify worktree ownership. Claude
  command `/semsparql-pickup`.

Graph-reading skill, adapted from SemEngine's for a read-only consumer:

- [query-pattern](skills/query-pattern/SKILL.md): choose how SemSparql reads the graph through `reader.GraphReader`.
  Claude command `/query-pattern`.

SemEngine's `entity-or-bucket` and `new-payload` skills are not carried: SemSparql writes nothing. `kv-or-stream`
joins only if the optional KV watch reader (issue #1, task 5.1) is built.

Codex can invoke the canonical names with `$`, or read their paths directly. Antigravity (Gemini) automatically
discovers `.agents/skills/<name>/SKILL.md` from the workspace root. Claude uses the command names above; its adapters
live in `.claude/skills/<command-name>/SKILL.md`. The `preflight` adapter deliberately maps to the canonical
`semsparql-preflight` name. Add a skill only when the code it governs exists or is being built. A private memory
reference cannot be required to interpret a shared rule.

## Read-only parity smoke

Steps 1–8 are enforced by `TestAgentSetupParity` and `TestAgentSetupParitySensitivity` in `task test:unit`. Run step 9
by hand after changing a contract, adapter, or repository routing rule.

1. Confirm all four canonical contracts and all eight agent adapters exist, and that `.agents/protocol.md` and
   `.agents/rules-index.md` exist.
2. Confirm each adapter names-and-links only its matching `.agents/contracts/...` path and says to read it fully first.
3. Confirm the Claude reviewer and architect tool lists contain `Read`, `Bash`, `Grep`, `Glob`, `Skill`, and that
   write adapters add `Edit` and `Write`, with no `Task`, `LSP`, or other delegation tool (`gopls` is reached through
   `Bash`; an unresolved tool name makes Claude Code refuse to launch the agent).
4. Confirm the Codex reviewer and architect set `sandbox_mode = "read-only"`; the developer and the technical writer
   have no sandbox override and inherit the parent workspace permissions.
5. Confirm `CLAUDE.md` only imports `AGENTS.md` and carries no rules of its own, that `AGENTS.md` stays strictly under
   24,000 bytes (`wc -c AGENTS.md`) so rule loaders with a 24 KB per-file cap do not truncate it, and that every rule
   row in `AGENTS.md` has its corresponding audit row in `.agents/rules-index.md` (same numbered domain headings and
   per-domain row counts; semantic correspondence of individual rows is review only).
6. Confirm role adapters stay within 20 lines and contain no copied checklist (`- [ ]` or `- [x]`).
7. For every skill in the shared-skills list, confirm the canonical file and Claude adapter exist, the adapter points
   to that canonical file and says to read it fully, and it contains no copied checklist.
8. Confirm the Claude and Codex model and effort settings match the table above.
9. Validate changed skill frontmatter and relative links. A structurally valid adapter does not prove its
   instructions make the right decisions; walk the applicable scenarios below without private memory.

Use these semantic fixtures when reading the routing text:

- "Implement a nontrivial change" routes first to the SemSparql developer and then the SemSparql reviewer.
- "Review a nontrivial SemSparql change" routes to the SemSparql reviewer in read-only mode.
- "Design an API contract or OpenSpec target" routes to the SemSparql architect (surface inventory first, drafts as
  text); binding rulings and approval stay with the owner session.
- "Update durable docs or reconcile task truth" routes to the SemSparql technical writer.
- "Check an isolated Go idiom" may use a generic Go agent only as a second pass.
- "Prepare a docs-only PR" retains protocol review/CI gates and selects checks for the changed artifacts.
- "The previous revision was green" preserves that evidence as historical, not current-head proof.
- "Continue without private memory" resolves shared state and verifies ownership through pickup/protocol.

The smoke passes only when Claude, Codex, and Gemini resolve the same logical role and canonical contract for every
fixture.
