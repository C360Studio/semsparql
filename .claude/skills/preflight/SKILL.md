---
name: preflight
description: >-
  Select and run the existing SemSparql verification gates for a concrete diff before an implementation push
  or local-readiness assessment.
argument-hint: "[optional explicit comparison base]"
---

# SemSparql preflight

Read [the canonical semsparql-preflight skill](../../../.agents/skills/semsparql-preflight/SKILL.md) fully and
follow it. Apply any explicit comparison base in `$ARGUMENTS`; otherwise establish the actual PR target as the
canonical skill directs. This adapter preserves `/preflight` and adds no separate gate or authority.
