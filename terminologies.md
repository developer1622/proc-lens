# Proc-Lens Terminology Guide

> **For junior developers and newcomers**  
> This page explains the key concepts, Linux internals, mathematical ideas, and tool-specific terms used in Proc-Lens in simple, beginner-friendly language.  
> Each term includes:  
> - **What it is** (plain English)  
> - **How it is useful here** (why Proc-Lens cares about it)  
> - **One good example** (from this project)

---

## Quick Index

**Core Concepts**  
[Agent](#agent) · [Observability](#observability) · [Telemetry](#telemetry) · [Workload](#workload) · [HLD Archetypes](#hld-archetypes-high-level-design-archetypes)

**Linux & Kernel Terms**  
[/proc Filesystem](#proc-filesystem) · [cgroups](#cgroups) · [sysctl](#sysctl) · [CAP_SYS_PTRACE](#cap_sys_ptrace) · [Context Switch](#context-switch) · [OOM Score](#oom-score) · [PSI (Pressure Stall Information)](#psi-pressure-stall-information) · [NUMA](#numa)

**Mathematics & Algorithms**  
[Cosine Similarity](#cosine-similarity) · [log1p](#log1p) · [Feature Vector](#feature-vector) · [Centroid](#centroid) · [Classification](#classification) · [SHA-256 Fingerprint](#sha-256-fingerprint) · [Shannon Entropy / Diversity Score](#shannon-entropy--diversity-score) · [Drift Detection](#drift-detection)

**Proc-Lens Specific Features**  
[Node Workload Fingerprint](#node-workload-fingerprint) · [Workload Drift](#workload-drift) · [Validated Kernel Optimization](#validated-kernel-optimization) · [Platform Capability Report](#platform-capability-report) · [Low-Cardinality Metrics](#low-cardinality-metrics) · [JSONL Output](#jsonl-output) · [Dual-Use (CLI + DaemonSet)](#dual-use-cli--daemonset) · [Command-line Redaction](#command-line-redaction)

**Deployment & Tooling**  
[DaemonSet](#daemonset) · [GoReleaser](#goreleaser) · [GHCR (GitHub Container Registry)](#ghcr-github-container-registry)

---

## Core Concepts

### Agent
**What it is**  
An "agent" is a small program that runs in the background on a machine (or inside every node in a cluster) and quietly collects information or performs small tasks without constant human intervention.

**How it is useful here**  
Proc-Lens can run as a long-lived agent (via `scan --loop`) that continuously watches processes on a server or Kubernetes node and reports what kinds of work are happening.

**Example**  
In production you deploy Proc-Lens as a Kubernetes DaemonSet (one agent on every node). It keeps scanning processes every 10 seconds and streams JSONL logs so your central logging system always knows the current "personality" of each node.

### Observability
**What it is**  
Observability means having enough information about a system so you can understand its internal state just by looking at the data it produces (logs, metrics, traces). It is stronger than simple "monitoring" because it lets you answer *why* something is happening, not just *that* it is happening.

**How it is useful here**  
Proc-Lens adds *semantic* observability: instead of just telling you "CPU is 80%", it tells you "this node is now 38% AITraining workloads" and "that change happened 45 seconds ago."

**Example**  
A traditional monitoring alert says "Node CPU high." Proc-Lens also emits a `workload_drift` event with severity `CRITICAL`, making it obvious that someone just deployed a big AI training job on a database node.

### Telemetry
**What it is**  
Telemetry is the raw data a system emits about itself — CPU usage, memory, open files, network connections, etc. Think of it as the "vital signs" of software and hardware.

**How it is useful here**  
Proc-Lens is fundamentally a telemetry collector + interpreter. It gathers process-level telemetry and turns the numbers into meaningful HLD categories.

**Example**  
The collector reads RSS memory, thread count, socket count, and I/O rates for every process. These raw numbers become the input to the cosine-similarity classifier.

### Workload
**What it is**  
A "workload" is the actual work a computer (or a single process) is doing — for example, "serving web requests", "running database queries", "training a neural network", or "compressing backup files".

**How it is useful here**  
Proc-Lens's entire purpose is to figure out the *type* of workload each process represents so that humans and automated systems can make better decisions.

**Example**  
Instead of seeing 50 processes called `python`, Proc-Lens tells you "12 of them are AITraining and 8 are AIInference." That distinction changes how you schedule, tune, or secure the node.

### HLD Archetypes (High-Level Design Archetypes)
**What it is**  
HLD archetypes are 16 standard "roles" or "shapes" that processes in large systems usually play (LoadBalancer, RelationalDB, CacheStore, AITraining, etc.). They come from classic system architecture thinking.

**How it is useful here**  
Proc-Lens classifies every running process into one of these 16 well-known roles. This turns raw numbers into architecture-level insight.

**Example**  
The 16 archetypes include: LoadBalancer, WebServer, CacheStore, RelationalDB, NoSQLDB, ColumnarDB, VectorDB, SearchEngine, MessageBroker, EventStreaming, AITraining, AIInference, OrchestratorAgent, MonitoringAgent, InteractiveShell, and UtilityBatch.

---

## Linux & Kernel Terms

### /proc Filesystem
**What it is**  
`/proc` is a special virtual filesystem on Linux that exposes information about running processes and the kernel as if they were normal files. You can `cat /proc/1234/status` to learn about process 1234.

**How it is useful here**  
Proc-Lens reads many files under `/proc` (and the per-process `/proc/<pid>/...` directories) to get rich details that normal tools miss: cgroups, OOM scores, file descriptor types, namespace IDs, etc.

**Example**  
`parseCgroupDetails()` reads `/proc/<pid>/cgroup` to discover which Kubernetes pod and container a process belongs to — without talking to the Kubernetes API.

### cgroups
**What it is**  
cgroups (control groups) are a Linux kernel feature that lets you limit, account for, and isolate resource usage (CPU, memory, I/O, etc.) of groups of processes.

**How it is useful here**  
Proc-Lens reads cgroup information to understand container boundaries and to enrich process data with pod/container metadata.

**Example**  
When a process is inside a Kubernetes pod, its cgroup path contains the pod UID. Proc-Lens parses this so the JSON output includes `pod_name`, `namespace`, and `container_id`.

### sysctl
**What it is**  
`sysctl` is the command (and the `/proc/sys` interface) used to view and change kernel parameters at runtime — things like TCP buffer sizes, dirty page writeback behavior, overcommit policy, etc.

**How it is useful here**  
The optimizer reads live values from `/proc/sys` (via sysctl paths) and compares them against recommended values for the detected workload type.

**Example**  
For a RelationalDB process, Proc-Lens checks `vm.dirty_background_ratio`. If the live value is higher than the recommended 5, it marks the recommendation as `[PENDING]` instead of blindly suggesting the change.

### CAP_SYS_PTRACE
**What it is**  
A Linux capability that allows a process to inspect and debug other processes (read their memory maps, open file descriptors, etc.) without being root.

**How it is useful here**  
This is the *only* capability Proc-Lens ever needs. Everything else runs with zero privileges.

**Example**  
The Helm chart adds only `CAP_SYS_PTRACE` and drops all others. This is why security teams usually approve Proc-Lens DaemonSets more easily than eBPF tools that need `CAP_BPF` + `CAP_SYS_ADMIN`.

### Context Switch
**What it is**  
When the CPU stops running one thread and starts running another, a "context switch" occurs. There are two kinds:
- **Voluntary**: the thread gave up the CPU (e.g., waiting for I/O or a lock).
- **Involuntary**: the kernel forcibly took the CPU away (usually because the thread used up its time slice).

**How it is useful here**  
High involuntary context switches often indicate CPU contention or oversubscription. Proc-Lens collects both rates and surfaces them in telemetry and recommendations.

**Example**  
A CacheStore process showing very high involuntary context switches may be suffering from CPU noise from neighboring AI jobs on the same node.

### OOM Score
**What it is**  
The Out-Of-Memory (OOM) killer in Linux assigns every process an "OOM score". Higher score = more likely to be killed when the system runs out of memory.

**How it is useful here**  
Proc-Lens reads `oom_score` and `oom_score_adj` so operators can see which processes are already marked as "kill me first" by the kernel.

**Example**  
A background batch job with a very high OOM score on a memory-tight database node is a red flag that the kernel already considers it sacrificial.

### PSI (Pressure Stall Information)
**What it is**  
A modern Linux kernel feature (since ~4.20) that tells you *how much time* tasks are being stalled because of CPU, memory, or I/O pressure. It has "some" (at least one task stalled) and "full" (all runnable tasks stalled) counters.

**How it is useful here**  
Raw CPU% can be high without anyone suffering. PSI tells you whether real work is being delayed. Proc-Lens exposes PSI when `--enable-psi` is used and reports it via the `capabilities` command.

**Example**  
`some cpu=12.4%` over the last 60 seconds means that for 12.4% of the time, at least one process wanted to run on the CPU but couldn't.

### NUMA
**What it is**  
Non-Uniform Memory Access. On big servers, memory is attached to specific CPU sockets. Accessing "local" memory is fast; "remote" memory is slower.

**How it is useful here**  
The `hardware` subcommand reports NUMA topology. The optimizer can then suggest NUMA-aware placement (`numactl`) for memory-sensitive workloads such as databases or AI training.

**Example**  
On a 4-NUMA-node server running a large VectorDB, the hardware report will show the node layout so operators know to bind the process with `numactl --cpunodebind=0 --localalloc`.

---

## Mathematics & Algorithms

### Cosine Similarity
**What it is**  
A way to measure how similar two vectors (lists of numbers) are by looking at the angle between them, ignoring their length. Result is always between -1 and 1 (we only see 0–1 here because all our features are non-negative).

**How it is useful here**  
Proc-Lens turns process telemetry into an 8-dimensional vector and compares it to 16 archetype "prototype" vectors using cosine similarity. Highest similarity wins.

**Example**  
A process vector that is very close in direction to the RelationalDB centroid gets classified as `RelationalDB` even if its absolute CPU or memory numbers are unusual.

### log1p
**What it is**  
A mathematical function: `log1p(x) = log(1 + x)`. It is the natural logarithm of one plus the input. It compresses very large numbers while keeping small numbers distinguishable.

**How it is useful here**  
Process metrics live on wildly different scales (bytes of memory vs. number of threads vs. percentage CPU). log1p brings them into roughly the same range so cosine similarity is not dominated by the biggest number.

**Example**  
A process with 8 GB RSS becomes `log1p(8192) ≈ 9.01`. A process with 50 threads becomes `log1p(50) ≈ 3.93`. Without log1p the memory number would completely dominate the similarity calculation.

### Feature Vector
**What it is**  
A list of numbers that describes an object (here: a process) in a way that a mathematical model can understand.

**How it is useful here**  
Proc-Lens builds an 8-element feature vector for every process: [CPU, Memory, Threads, Sockets, FDs, IORead, IOWrite, CtxSwitches], all after log1p scaling.

**Example**  
A Redis process might produce the vector `[2.1, 6.8, 1.6, 6.7, 6.8, 0.0, 0.1, 8.7]` after scaling. This vector is then compared to the CacheStore centroid.

### Centroid
**What it is**  
In this context, a "prototype" or "average example" vector for a category. Think of it as the "typical" shape of a RelationalDB process, a LoadBalancer process, etc.

**How it is useful here**  
Proc-Lens stores one centroid per HLD archetype. Classification is literally "which of these 16 prototype shapes is this process most similar to?"

**Example**  
The RelationalDB centroid has relatively high memory and I/O values. The InteractiveShell centroid has extremely low values across the board. A real postgres process will be much closer to the first centroid than the second.

### Classification
**What it is**  
The act of assigning a label (or "class") to something based on its characteristics.

**How it is useful here**  
Proc-Lens classifies every running process into one of 16 HLD architecture roles so that higher-level decisions (tuning, scheduling, cost attribution, alerting) can be made in human-understandable terms.

**Example**  
A process that looks like a database (high memory + I/O + name contains "postgres") is classified as `RelationalDB`.

### SHA-256 Fingerprint
**What it is**  
A cryptographic hash (fingerprint) that turns a bunch of data into a short, fixed-size string of characters. The same input always produces the same output; tiny changes in input produce completely different outputs.

**How it is useful here**  
Proc-Lens builds a "workload identity" of the node (the mix of HLD categories) and hashes it. Two nodes running the same mix of database + cache + web workloads will have the *exact same* fingerprint even if the actual PIDs and start times are different.

**Example**  
`fingerprint_hash: "a3f9b2c1..."` appears in every JSON prediction and in the `node_fingerprint` JSONL event. You can group or alert on this hash across hundreds of nodes.

### Shannon Entropy / Diversity Score
**What it is**  
A measure (from information theory) of how "mixed" or "diverse" a set of things is. 0.0 means everything is the same; higher values (approaching 1.0 in our normalized usage) mean many different things are present in roughly equal proportions.

**How it is useful here**  
Proc-Lens includes a diversity score with every fingerprint. A score near 0 means the node is dominated by one archetype (e.g., pure database node). A higher score means a more mixed workload.

**Example**  
A node that is 95% RelationalDB will have a very low diversity score. A node running web servers + caches + a bit of AI inference will have a noticeably higher score.

### Drift Detection
**What it is**  
The act of noticing that something has changed in a meaningful way over time.

**How it is useful here**  
Proc-Lens compares the current node's workload fingerprint against the previous one. When the mix of archetypes shifts by more than 5 percentage points in any category, it emits a structured `workload_drift` event.

**Example**  
At 10:00 the node was 100% RelationalDB. At 10:05 it is 62% RelationalDB + 38% AITraining. Proc-Lens emits a `CRITICAL` drift event with the exact category changes.

---

## Proc-Lens Specific Features

### Node Workload Fingerprint
(See SHA-256 Fingerprint + Diversity Score above.)

### Workload Drift
(See Drift Detection above.)

### Validated Kernel Optimization
**What it is**  
Instead of blindly suggesting "run this sysctl command", the tool reads the *current* value of the kernel parameter on the actual running system and tells you whether the recommendation is already satisfied or still needed.

**How it is useful here**  
The optimizer produces recommendations that are safe to feed into automation because they have already been checked against reality.

**Example**  
For a CacheStore the tool recommends `vm.overcommit_memory=1`. It then reads the live value. If it is already 1, the recommendation is emitted as `[ALREADY APPLIED]`. If it is still 0, it is marked `[PENDING]`.

### Platform Capability Report
**What it is**  
A machine-readable report (via the `capabilities` subcommand) that declares, for the exact machine you are on right now, which Proc-Lens features are fully working, partially working, or completely unavailable.

**How it is useful here**  
It removes the guesswork when you deploy the same binary across Linux servers, developer macOS laptops, Windows boxes, and minimal containers.

**Example**  
```json
{"name":"psi","status":"UNAVAILABLE","limitation_note":"PSI requires Linux kernel >= 4.20 with psi=1 on the kernel command line"}
```

### Low-Cardinality Metrics
**What it is**  
Prometheus metrics that use only a small number of label values (in Proc-Lens this means category names like `RelationalDB`, never raw PIDs or process names).

**How it is useful here**  
High-cardinality labels (one label per PID) destroy Prometheus performance and storage. Proc-Lens deliberately emits only low-cardinality series so the metrics are safe to scrape from every node in a large fleet.

**Example**  
`proc_lens_predictions{category="RelationalDB"} 47` instead of 47 separate metrics with `pid="1234"`, `pid="5678"`, etc.

### JSONL Output
**What it is**  
JSON Lines — one JSON object per line. Extremely easy for log shippers (Fluent Bit, Vector, Filebeat, etc.) to parse and forward.

**How it is useful here**  
`scan --format json` and drift/fingerprint events are emitted as JSONL so they flow naturally into your existing logging or observability pipeline.

**Example**  
```json
{"event_type":"node_fingerprint","fingerprint":{"fingerprint_hash":"a3f9b2c1..."},"node_context":{...}}
{"event_type":"workload_drift","severity":"CRITICAL",...}
```

### Dual-Use (CLI + DaemonSet)
**What it is**  
The exact same binary is useful both as an interactive command-line tool on a laptop and as a long-running background service (agent) on production nodes.

**How it is useful here**  
Developers use `proc-lens run`, `explain`, or `analyze` on their machines. The exact same binary is deployed fleet-wide as a DaemonSet with `scan --loop --format json`.

**Example**  
On your laptop: `./proc-lens explain --pid 8842`  
In production: the DaemonSet runs `proc-lens scan --loop --interval 10s --format json`

### Command-line Redaction
**What it is**  
By default, Proc-Lens hides everything after the program name in a process's command line (`/usr/bin/postgres -D /data` becomes `/usr/bin/postgres [REDACTED]`).

**How it is useful here**  
Prevents accidental leakage of passwords, tokens, or connection strings that are often passed on the command line.

**Example**  
`--expose-cmdline` is the escape hatch for the rare case when you *do* need the full command line (and you understand the risk).

---

## Deployment & Tooling

### DaemonSet
**What it is**  
A Kubernetes workload that ensures exactly one copy of a pod runs on every (or selected) node in the cluster.

**How it is useful here**  
Proc-Lens is designed to be deployed as a DaemonSet so there is one agent on every node, continuously providing workload intelligence for the entire fleet.

**Example**  
```yaml
kind: DaemonSet
spec:
  template:
    spec:
      hostPID: true
      containers:
      - name: proc-lens
        args: ["scan", "--loop", "--format", "json"]
```

### GoReleaser
**What it is**  
A popular open-source tool that automates building, packaging, and releasing Go binaries and container images across many platforms and architectures.

**How it is useful here**  
The project's release pipeline (`.goreleaser.yml` + GitHub Actions) uses GoReleaser to produce static binaries for Linux/macOS/Windows (amd64 + arm64), multi-arch container images, checksums, and SBOMs on every version tag.

**Example**  
`goreleaser release --clean` produces the artifacts that end up in GitHub Releases and on GHCR.

### GHCR (GitHub Container Registry)
**What it is**  
GitHub's hosted container image registry (images live at `ghcr.io`).

**How it is useful here**  
Official multi-arch images of Proc-Lens are published to `ghcr.io/developer1622/proc-lens` (or your own org) so teams can pull them without maintaining their own registry.

**Example**  
```bash
docker pull ghcr.io/developer1622/proc-lens:latest
```

---

**This page is intentionally written as a living terminology reference.**  
Feel free to suggest new terms or improvements as the project evolves. All explanations are intentionally kept at a junior-to-mid level while still being technically precise for the concepts that actually appear in the Proc-Lens source code, tests, and documentation.