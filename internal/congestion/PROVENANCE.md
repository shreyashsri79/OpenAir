# Vendored code in `internal/congestion`

`bbr/` and `common/` are copied from Hysteria, not imported.

| | |
|---|---|
| Upstream | `github.com/apernet/hysteria/core/v2` |
| Version | `v2.10.0` |
| Paths | `internal/congestion/bbr/`, `internal/congestion/common/` |
| Licence | MIT — `LICENSE.hysteria.md`, retained verbatim |

## Why this is a copy and not a dependency

D-16 chose this BBR and described the plan as "the dependency therefore becomes
apernet/quic-go plus hysteria's BBR". Half of that is not possible. Hysteria's
BBR lives under `core/v2/**internal**/congestion/bbr`, and Go's internal-package
rule makes it unimportable from outside the `core/v2` module. There is no
exported path to it, so the choice is to copy it or to not use it.

What D-16 got right is the injection point: the apernet fork exposes
`(*quic.Conn).SetCongestionControl(congestion.CongestionControl)` as public API,
so the controller can be installed on a live connection without patching
quic-go's `internal/`. That is the part that makes this cheap, and it survives.

D-35 records the correction.

## Which fork, and why the version looks odd

`github.com/apernet/quic-go v0.60.1-0.20260618182935-599b15a1fa26` — a
pseudo-version, deliberately, and it must not be "tidied up" to a release tag.

apernet publish two kinds of version. Their *tagged* releases (`v0.61.0` and so
on) mirror upstream and still declare `module github.com/quic-go/quic-go`, so
they cannot be required under the `github.com/apernet/quic-go` path at all. Only
their branch head declares the apernet module path, and only it carries the
exported `congestion/` and `monotime/` packages this code needs. The pseudo-
version is therefore the real release. Hysteria v2.10.0 pins the same commit.

Anyone upgrading this should check that `congestion/` still exists in the target
version before bumping — if apernet stop shipping it, the vendored BBR stops
having anywhere to install itself.

## Local modifications

Kept to the minimum, so that re-syncing stays a diff rather than a merge:

1. Import path rewritten: `github.com/apernet/hysteria/core/v2/internal/congestion/common`
   becomes `github.com/shreyashsri79/openair/internal/congestion/common`.
2. Debug environment variable renamed `HYSTERIA_BBR_DEBUG` to `OPENAIR_BBR_DEBUG`.

Nothing else. The algorithm is untouched, and `bbr/bbr_sender_test.go` is
upstream's own test, kept because it is the check that the port is faithful.

## Re-syncing

```
go list -m -versions github.com/apernet/hysteria/core/v2
# copy internal/congestion/{bbr,common} from the module cache, then re-apply
# the two modifications above and run go test ./internal/congestion/...
```

`golang.org/x/exp` is a dependency of this vendored code alone
(`bbr/windowed_filter.go` uses `constraints.Integer | constraints.Float`, which
has no `cmp` equivalent because `cmp.Ordered` admits strings). If this package
goes, so does that dependency.
