# Contributing to ProcLens

Thank you for considering a contribution to ProcLens! This document covers everything you need to know to get started quickly and contribute effectively.

> 📖 **New here?** Read [terminologies.md](terminologies.md) for plain-English explanations of all concepts used in this codebase, and [how-to-run-this-repo.md](how-to-run-this-repo.md) for environment setup.

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Ways to Contribute](#ways-to-contribute)
- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Branch & Commit Conventions](#branch--commit-conventions)
- [Adding a New Workload Archetype](#adding-a-new-workload-archetype)
- [Pull Request Process](#pull-request-process)
- [Testing Requirements](#testing-requirements)
- [Code Style](#code-style)
- [Reporting Issues](#reporting-issues)

---

## Code of Conduct

All contributors are expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md). We are committed to keeping this a welcoming, respectful, and productive community.

---

## Ways to Contribute

| Type | Examples |
|---|---|
| 🐛 **Bug fix** | Fix a panic, wrong classification, or incorrect recommendation |
| ✨ **New feature** | Add a new archetype, new LLM provider, new command |
| 📖 **Docs** | Improve README, terminologies, how-to-run, or inline comments |
| 🧪 **Tests** | Add missing table-driven tests, fuzz seeds, or benchmark cases |
| 🎨 **UI/UX** | Improve terminal colour output or help text |
| ⚡ **Performance** | Reduce per-PID collection latency or memory allocations |

---

## Development Setup

```bash
# 1. Fork the repo on GitHub, then clone your fork
git clone https://github.com/<your-username>/proc-lens.git
cd proc-lens

# 2. Ensure Go 1.24+ is installed
go version

# 3. Download dependencies
go mod download

# 4. Build the binary
go build -o proc-lens ./cmd/proc-lens

# 5. Run the full test suite
go test ./... -count=1

# 6. Run a quick smoke test
./proc-lens scan -d 1s
```

---

## Project Structure

```
proc-lens/
├── cmd/proc-lens/          # Main entrypoint
├── pkg/
│   ├── classifier/         # 37-archetype cosine-similarity classifier
│   │   ├── types.go        # Category constants (one per archetype)
│   │   ├── centroids.go    # Reference feature vectors per archetype
│   │   ├── classifier.go   # Predict() + rule boosters
│   │   └── classifier_test.go
│   ├── collector/          # /proc telemetry collection (gopsutil)
│   ├── cmd/                # Cobra subcommands (scan, analyze, enrich, ...)
│   ├── optimizer/          # Kernel tuning recommendations + validation
│   ├── explain/            # "Why was this classified X?" explainer
│   ├── pressure/           # Linux PSI collection
│   ├── hardware/           # NUMA / storage / SIMD topology
│   ├── completeness/       # Data completeness score tracker
│   └── metrics/            # Prometheus low-cardinality metrics
├── deploy/proc-lens/       # Helm chart (DaemonSet)
├── .goreleaser.yml         # GoReleaser v2 release pipeline
├── terminologies.md        # Plain-English concept glossary
├── how-to-run-this-repo.md # Step-by-step setup guide
└── CONTRIBUTING.md         # This file
```

---

## Branch & Commit Conventions

**Branch naming:**
```
feat/add-graphdb-archetype
fix/context-canceled-enrich
docs/update-readme-archetypes
test/fuzz-classifier-edge-cases
chore/goreleaser-v2-migration
```

**Commit messages** follow [Conventional Commits](https://www.conventionalcommits.org/):
```
feat(classifier): add GraphDB and TimeSeriesDB archetypes
fix(enrich): use gemini-flash-latest model alias, fix context cancellation
docs(readme): update archetype table to 37 categories
test(classifier): add table-driven tests for all new archetypes
```

---

## Adding a New Workload Archetype

Adding a new archetype requires changes in exactly **4 files** — always in this order:

### Step 1 — `pkg/classifier/types.go`

Add the `Category` constant with a comment listing real-world examples:

```go
// In the appropriate tier const block:
MyNewArchetype Category = "MyNewArchetype" // e.g. SomeProcess, AnotherProcess
```

### Step 2 — `pkg/classifier/centroids.go`

Add the centroid to the `Centroids` map. Choose physical telemetry values that represent the "ideal" profile for this archetype:

```go
MyNewArchetype: makeCentroid(
    cpu,        // typical CPU% (e.g. 20.0)
    memMb,      // typical RSS in MB (e.g. 512.0)
    threads,    // typical thread count (e.g. 16.0)
    sockets,    // typical socket count (e.g. 30.0)
    fds,        // typical FD count (e.g. 200.0)
    ioReadKb,   // typical read KB/s (e.g. 1000.0)
    ioWriteKb,  // typical write KB/s (e.g. 500.0)
    ctxSw,      // typical ctx switches/sec (e.g. 800.0)
),
```

### Step 3 — `pkg/classifier/classifier.go`

Add name/cmdline keyword boosters in the appropriate tier section:

```go
if containsAny(name, "myprocess", "myd") {
    scores[MyNewArchetype] += 0.25
    rulesTriggered = append(rulesTriggered,
        fmt.Sprintf("Signature: MyNewArchetype keyword in process name '%s'", stats.Name))
}
```

Also add `MyNewArchetype` to the centroid completeness test list in `classifier_test.go`.

### Step 4 — `pkg/classifier/classifier_test.go`

Add at least one table-driven test case in `TestPredictNewArchetypes`:

```go
{
    desc: "MyProcess workload",
    stats: collector.ProcessStats{
        Name:    "myprocess",
        Cmdline: "myprocess --config /etc/myprocess.conf",
        // ... realistic telemetry ...
    },
    expectedCategory: MyNewArchetype,
    minConfidence:    0.5,
},
```

Also add the constant to `TestCentroids_AllCategoriesHaveCentroid`.

### Step 5 — Update README and terminologies

- Add the archetype to the **37 Workload Archetypes** table in `README.md`
- If it introduces new concepts, add an entry to `terminologies.md`

---

## Pull Request Process

1. **Fork** the repository and create your branch from `main`.
2. **Run the full test suite** — `go test ./... -count=1` must pass with zero failures.
3. **Run the build** — `go build ./...` must compile clean.
4. **Write or update tests** — new archetypes require table-driven tests; new features require unit tests.
5. **Update documentation** if the change affects user-facing behaviour (README, terminologies, help text).
6. **Open a Pull Request** with:
   - A clear title following the Conventional Commits format
   - A description explaining *what* changed and *why*
   - Reference to any related GitHub Issues (`Closes #123`)

A maintainer will review and respond within a reasonable time. We may request changes before merging.

---

## Testing Requirements

| Requirement | Command |
|---|---|
| All tests pass | `go test ./... -count=1` |
| Classifier tests pass | `go test ./pkg/classifier/... -v` |
| No build errors | `go build ./...` |
| Fuzz check (optional but appreciated) | `go test ./pkg/classifier -fuzz=FuzzPredict -fuzztime=30s` |
| Benchmarks (for performance changes) | `go test ./pkg/classifier -bench=. -run=^$` |

**Key test invariants that must always hold:**
- `Predict(collector.ProcessStats{})` must never panic (zero-value safety)
- All confidence values must be in `[0.0, 1.0]`
- Every `Category` constant must have a corresponding entry in `Centroids`
- `containsAny("anything", "")` must return `false` (empty target guard)

---

## Code Style

- Follow standard Go conventions: `gofmt`, `go vet`, meaningful variable names.
- Use **table-driven tests** with `t.Run()` for all test cases.
- Add a **package-level comment block** with `Note:` and `Caveat:` sections on new files.
- Error messages should be lower-case and actionable (e.g. `"failed to read /proc/stat: %w"`).
- Avoid informal language in code comments ("kindly", "do the needful", etc.).
- Security-sensitive changes (new network calls, new file reads) must include a brief security rationale comment.

---

## Reporting Issues

- Use [GitHub Issues](https://github.com/developer1622/proc-lens/issues) to report bugs, suggest features, or ask questions.
- For bug reports, include:
  - ProcLens version (`./proc-lens --version`)
  - OS and kernel version (`uname -a`)
  - The exact command that failed
  - Full error output (stderr included)
- For security vulnerabilities, please use GitHub's **Private Security Advisory** feature rather than opening a public issue.

---

We look forward to your contribution. ProcLens is a learning-focused, production-grade project — the codebase is intentionally well-commented and structured to be approachable for engineers at all levels.
