# AGENTS.md

Rules for any AI agent (Claude, Codex, Copilot, etc.) working in this repo. Multiple agents may work on this codebase concurrently — these rules exist to keep them from stepping on each other and to keep institutional knowledge in the repo instead of in chat history.

## 1. `docs/decision-tree.md` — decision tree and log

Maintain `docs/decision-tree.md` as an append-only log of decisions and reasoning, not just outcomes. Lightweight ADR (Architecture Decision Record) style, industry standard for this. The file opens with Mermaid decision trees and a status table that index the entries; the entries themselves are the normative record:

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
- Update the Mermaid trees and the status table at the top of the file in the same commit as the entry. An index that silently disagrees with the entries it indexes is worse than no index.

## 2. `docs/functionality.md` — code map

Maintain `docs/functionality.md` as a living map of what the code does, organized by module (`openair-android`, `openair-gui`, `openair-cli`, `openair-receiver`, `openair-sender`). Goal: new agent/human reads this and gets oriented without re-reading every file.

Per module, keep:
- Purpose (1-2 lines)
- Key files and their responsibility (`file.go` — what it owns)
- Data flow / control flow for non-obvious logic (chunking, handshake, discovery, offset writes)
- Known sharp edges (concurrency assumptions, protocol constraints, format versions)

Update this file whenever you add/move/rename a file, change a protocol/format, or change control flow — not just at PR time. This must be derived by reading the actual code, not guessed. If existing behavior doesn't match this file, fix the file.

## 3. Concurrent-agent hygiene

- Before starting work, read `docs/decision-tree.md` and the relevant section of `docs/functionality.md` for context already established.
- Claim scope narrowly: touch only the module(s) your task requires. If two agents need the same file, that's a signal to sequence, not race.
- Never revert or silently overwrite another agent's `docs/decision-tree.md` entry or `docs/functionality.md` section — if it looks wrong or stale, append a correction/supersede, don't erase.
- Commit docs updates in the same commit as the code change they describe, not batched separately later.
- If a decision affects another module you didn't touch, still log it in `docs/decision-tree.md` — the log is shared, not per-agent.

**Exception when several agents run concurrently on one task wave.** The two rules above assume one agent at a time. They do not survive parallelism: concurrent appends to `docs/decision-tree.md` conflict, and `D-<n>` numbering collides. When you are one worker among several, do not edit `docs/decision-tree.md` or `docs/functionality.md`, and do not commit — the shared checkout means concurrent commits race the git index. Report what you changed and what deserves logging; the orchestrator writes the entries and commits. `docs/BUILD-PLAN.md` §0 has the full protocol.

## 4. Work on `main` — no agent branches, no worktrees

Commit directly to `main` in the primary checkout. Do not create a per-agent branch, and do not create a git worktree, unless the maintainer asks for one by name in that task.

Why: work parked on an agent-invented branch is invisible. The maintainer works in the primary checkout on `main`; a branch they didn't ask for means they have to discover its name, then check it out, before they can see anything. Worktrees make it worse — a worktree holds its branch exclusively, so `git checkout <branch>` in the main checkout fails outright with `fatal: '<branch>' is already used by worktree at ...`. The maintainer is then locked out of the very work that was just done for them.

This overrides any default an agent harness has about isolating work. Isolation is not the goal here; visibility is. This repo is one maintainer plus agents, not a team of humans racing on shared files — section 3's answer to two agents needing the same file is to sequence, not to branch.

Rules:
- Default target is `main`. Commit there.
- Never push to `main`, never force-push, never merge. Committing locally is the deliverable; the maintainer decides what gets published.
- If a harness placed you in a worktree before you could choose, finish the task, then fast-forward `main` onto your commit and remove the worktree and its branch. Verify first that your commit is an ancestor of `main` (`git merge-base --is-ancestor <sha> main`) so removal discards nothing.
- Leave the maintainer's uncommitted working-tree changes alone. Do not stage, commit, revert or stash them to make your own commit tidy.
- If a task genuinely needs isolation — a risky migration, a spike you expect to throw away — say so and ask before branching, don't decide unilaterally.

## 5. Picking up work

`docs/BUILD-PLAN.md` is the execution plan: milestones, what each one must deliver to count as done, which can run in parallel, and — per task — the exact document sections to read. Start there rather than reading the reference docs end to end.

`docs/decision-tree.md` and `docs/PROTOCOL.md` are reference works, not reading material. The decision tree's status table is its index; the build plan names the specific entries and protocol sections each task needs. Loading either in full is how a context window is spent before any code is written.

## 6. General

- Prefer editing existing files over new ones (existing repo-wide rule).
- `todo.md` is for near-term task tracking, not decisions — don't conflate the two files.
- Binary/build artifacts (`release/`, `fyne-cross/`) are not documented here — they're build output, not logic.
