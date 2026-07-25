# AGENTS.md

Rules for any AI agent (Claude, Codex, Copilot, etc.) working in this repo. Multiple agents may work on this codebase concurrently — these rules exist to keep them from stepping on each other and to keep institutional knowledge in the repo instead of in chat history.

## 1. `docs/decisions.md` — decision log

Maintain `docs/decisions.md` as an append-only log of decisions and reasoning, not just outcomes. Lightweight ADR (Architecture Decision Record) style, industry standard for this:

```
## D-<n>: <short title>
Date: YYYY-MM-DD
Status: proposed | accepted | superseded by D-<m>
Context: why this came up
Decision: what was chosen
Alternatives considered: what else, why rejected
Consequences: tradeoffs, follow-up work created
```

Rules:
- Append only. Never rewrite or delete past entries — if a decision changes, add a new entry and mark the old one `superseded by D-<m>`.
- One entry per real decision (architecture, protocol, dependency choice, tradeoff), not every commit.
- Number sequentially, never reuse `D-<n>`.
- Write it *before or during* the change, not reconstructed after the fact from memory.

## 2. `docs/functionality.md` — code map

Maintain `docs/functionality.md` as a living map of what the code does, organized by module (`openair-android`, `openair-gui`, `openair-cli`, `openair-receiver`, `openair-sender`). Goal: new agent/human reads this and gets oriented without re-reading every file.

Per module, keep:
- Purpose (1-2 lines)
- Key files and their responsibility (`file.go` — what it owns)
- Data flow / control flow for non-obvious logic (chunking, handshake, discovery, offset writes)
- Known sharp edges (concurrency assumptions, protocol constraints, format versions)

Update this file whenever you add/move/rename a file, change a protocol/format, or change control flow — not just at PR time. This must be derived by reading the actual code, not guessed. If existing behavior doesn't match this file, fix the file.

## 3. Concurrent-agent hygiene

- Before starting work, read `docs/decisions.md` and the relevant section of `docs/functionality.md` for context already established.
- Claim scope narrowly: touch only the module(s) your task requires. If two agents need the same file, that's a signal to sequence, not race.
- Never revert or silently overwrite another agent's `docs/decisions.md` entry or `docs/functionality.md` section — if it looks wrong or stale, append a correction/supersede, don't erase.
- Commit docs updates in the same commit as the code change they describe, not batched separately later.
- If a decision affects another module you didn't touch, still log it in `docs/decisions.md` — the log is shared, not per-agent.

## 4. General

- Prefer editing existing files over new ones (existing repo-wide rule).
- `todo.md` is for near-term task tracking, not decisions — don't conflate the two files.
- Binary/build artifacts (`release/`, `fyne-cross/`) are not documented here — they're build output, not logic.
