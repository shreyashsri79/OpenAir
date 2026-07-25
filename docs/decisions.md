# Decision Log

Append-only. See AGENTS.md for format and rules. Do not delete or rewrite past entries.

## D-1: Adopt AGENTS.md + decision log convention
Date: 2026-07-25
Status: accepted
Context: Multiple agents work this repo concurrently; decisions and code logic were living only in commit messages/chat, not discoverable by a fresh agent or contributor.
Decision: Introduce `AGENTS.md` (agent rules), `docs/decisions.md` (this file, ADR-style log), and `docs/functionality.md` (living code map). All agents must read/update these going forward.
Alternatives considered: Rely on commit messages only (rejected — not searchable/summarized, no rationale trail); full ADR tool/format like adr-tools (rejected — overkill for project size, plain markdown is enough).
Consequences: Every future non-trivial decision must be logged here; functionality.md must be kept in sync with code changes.

## D-2: Drop openair-cli, openair-receiver, openair-sender; focus on gui + android only
Date: 2026-07-25
Status: accepted
Context: Repo had four parallel/duplicated Go implementations (standalone sender, standalone receiver, cli, and gui's own internal/sender+receiver) per D-1's open question in functionality.md — unclear ownership, redundant maintenance surface. Project direction narrowed to just the desktop GUI and Android app.
Decision: Delete `openair-cli/`, `openair-receiver/`, `openair-sender/` and their release artifacts (`release/cli/`) entirely. Remove the CLI cross-compile block from `build_release.sh`. `openair-gui` (with its own `internal/sender`, `internal/receiver`) and `openair-android` are now the only supported clients.
Alternatives considered: Keep CLI as a thin wrapper around gui's internal packages (rejected — not asked for, adds scope); archive instead of delete (rejected — user asked for deletion, git history preserves it if needed).
Consequences: `build_release.sh` only builds gui + android. Any protocol/library code shared logic previously duplicated in cli/receiver/sender now lives solely in `openair-gui/internal/*`.
