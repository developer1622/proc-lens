# Deployment and Execution Guide: proc-lens

This guide provides instructions on how to configure and run the `proc-lens` process monitoring and classification tool across various environments, including bare-metal Linux virtual machines (VMs), Docker containers, and Kubernetes clusters.

---

## 1. Dual-Engine Architecture: With or Without AI

`proc-lens` is designed with a decoupled dual-engine architecture. It can run in a completely local, offline mode or in an LLM-enriched analysis mode.

### Mode A: Local Heuristic Engine (No AI / Offline)
By default, all core subcommands (`scan`, `analyze`, `run`) run 100% locally.
*   **Zero Dependencies**: Requires no API keys, no network connections, and no machine learning runtimes (like PyTorch or TensorFlow).
*   **Low Overhead**: Classification is done using local logarithmic cosine similarity calculations and process name signatures in less than 6 microseconds per process.
*   **Usage**: Ideal for air-gapped systems, local debugging, or edge collection agents.

### Mode B: LLM-Augmented Engine (With AI / Enrichment)
The `enrich` subcommand acts as a "second brain" that overlays the local telemetry with generative SRE diagnostics.
*   **Enriched Diagnostics**: Takes the local classification predictions, wraps them in a host environment context (RAM, cores, kernel version), and queries a frontier LLM API.
*   **Insights Provided**: Executive node utilization summaries, cross-process bottleneck correlations, cost/performance risks, and prioritized optimization commands with verification steps.
*   **Supported Providers**: Gemini, OpenAI, Anthropic Claude, xAI Grok, and local/self-hosted Ollama instances.

---

## 2. Platform Compatibility & Graceful Degradation

`proc-lens` is fully cross-platform and compiles for Linux, Windows, and macOS. Telemetry collection is designed to degrade gracefully depending on the host operating system and execution privileges:

| Telemetry Attribute | Linux VM (Root) | Linux VM (User) | Windows VM | macOS VM | Docker / K8s Container |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **CPU / RSS / Threads** | Yes | Yes | Yes | Yes | Yes |
| **Voluntary/Involuntary Ctx**| Yes | Yes | No | No | Yes |
| **Open FD Counts** | Yes | Only Own | Yes | Yes | Yes |
| **Socket Fast Path** | Yes | Only Own | No (Fallback) | No (Fallback) | Yes (via SYS_PTRACE) |
| **Parent PID & Name** | Yes | Yes | Yes | Yes | Yes |
| **Scheduling Nice / UIDs** | Yes | Yes | Yes | Yes | Yes |
| **Linux OOM Score & Adj** | Yes | Yes | No | No | Yes |
| **cgroups (v1/v2) Paths** | Yes | Yes | No | No | Yes |
| **Kubernetes Metadata** | No | No | No | No | Yes (via `/var/log/containers`) |

### Graceful Degradation Mechanics:
If a metric is unsupported by the OS (e.g. OOM scores on Windows) or blocked by permissions (e.g. reading another user's FDs as a non-root user), the collector **does not fail or crash**. It logs a debug warning, defaults the missing fields to `0` or empty strings, and continues executing normally.

---

## 3. How to Run Locally (Bare Linux, Windows, or macOS VMs)

### Step 1: Run Local Predictions (Offline / No AI)

#### Scan all running processes (One-shot):
```bash
./proc-lens scan -c 0.1 -m 5.0 -d 1s
```
*Calculates process activity rates over a 1-second window and lists active processes consuming >0.1% CPU or >5.0 MB RAM.*

#### Scan in continuous loop mode (Daemon/Top-like dashboard):
```bash
./proc-lens scan --loop --interval 10s
```
*In loop mode, rates are estimated using cached metrics from the previous iteration to eliminate double-sampling overhead.*

#### Profile a specific process PID:
```bash
./proc-lens analyze --pid 1024 --duration 2s
```

#### Profile a launched command:
```bash
./proc-lens run --cmd "tar -czf backup.tar.gz /var/log" --duration 3s --allow-run
```

---

### Step 2: Run LLM-Augmented Predictions (Enrichment)

To run the AI-enrichment engine, set the API key for your chosen provider and run the `enrich` subcommand.

#### Option A: Using Gemini API (Default)
```bash
export GEMINI_API_KEY="your-gemini-api-key"
./proc-lens enrich --top 5 --allow-remote-llm
```

#### Option B: Using xAI Grok API
```bash
export GROK_API_KEY="your-grok-api-key"
./proc-lens enrich --provider grok --model grok-2 --top 5 --allow-remote-llm
```

#### Option C: Using Anthropic Claude API
```bash
export ANTHROPIC_API_KEY="your-claude-api-key"
./proc-lens enrich --provider claude --model claude-3-5-sonnet-20241022 --top 5 --allow-remote-llm
```

#### Option D: Using Local Ollama (Air-gapped / Local AI)
If you do not want to transmit data to external APIs, you can run a local Ollama instance (e.g. running `llama3` locally on `localhost:11434`):
```bash
./proc-lens enrich --endpoint "http://localhost:11434/v1/chat/completions" --model "llama3" --top 5
```
*Note: No API key is required and no security flags are needed when running Ollama locally (since it is a localhost-only endpoint).*

---

## 4. How to Run inside Docker

The container runs as the unprivileged user `nobody` (UID 65534) by default. To inspect the host's processes rather than the container's isolated namespace, you must pass the host's PID namespace and mount the host's `/proc` and `/sys` filesystems.

### Run Local Predictions (JSONL Logging)
```bash
docker run -d --name proc-lens \
  --pid=host --cap-add=SYS_PTRACE \
  -v /proc:/host/proc:ro -v /sys:/host/sys:ro \
  -e HOST_PROC=/host/proc -e HOST_SYS=/host/sys \
  proc-lens:latest
```

### Run Enriched Diagnostics via Docker (Passing API Keys)
```bash
docker run --rm -it \
  --pid=host --cap-add=SYS_PTRACE \
  -v /proc:/host/proc:ro -v /sys:/host/sys:ro \
  -e HOST_PROC=/host/proc -e HOST_SYS=/host/sys \
  -e GEMINI_API_KEY="your-gemini-key" \
  proc-lens:latest enrich --top 5 --allow-remote-llm
```

---

## 5. How to Run inside Kubernetes (DaemonSet)

Deploying `proc-lens` as a Kubernetes DaemonSet allows you to gather structured process intelligence from every worker node in the cluster.

### Step 1: Configure Values
Review the Helm values file under `deploy/proc-lens/values.yaml` to ensure it is configured for least-privileged baseline execution:
```yaml
# deploy/proc-lens/values.yaml
hostNetwork: false   # Disabled by default
podSecurityContext:
  runAsUser: 65534   # Run as 'nobody'
  runAsGroup: 65534
  fsGroup: 65534
securityContext:
  privileged: false  # Avoid running as privileged
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  runAsNonRoot: true
  capabilities:
    add:
      - SYS_PTRACE   # Minimum capability required to read host /proc/<pid> files
    drop:
      - ALL
```

### Step 2: Deploy to Cluster
```bash
# Lint the Helm chart templates
helm lint deploy/proc-lens

# Install/upgrade the DaemonSet to the kube-system namespace
helm upgrade --install proc-lens deploy/proc-lens --namespace kube-system
```

### Step 3: Verify Telemetry and Health Checks
The DaemonSet container spins up a single, unified HTTP endpoint on port `8091`:
*   **Liveness/Readiness**: `/healthz` endpoint monitored by Kubernetes to verify the agent process is running smoothly.
*   **Unified Prometheus Telemetry**: `/metrics` endpoint exposing the 11 low-cardinality Prometheus metrics (scans_total, collection_errors_total, processes_classified_total, k8s_metadata_enrichment_total, processes_scanned, predictions, agent_cpu_usage_percent, agent_memory_rss_bytes, k8s_metadata_success_rate, collection_duration_seconds, and per_process_collection_seconds).
*   **Cardinality Protection**: To prevent index bloat on Prometheus, metrics do not contain high-cardinality labels (like individual `pid` or dynamic process names). Instead, they are aggregated by logical components like `category` (e.g. `RelationalDB`, `WebServer`) and status fields.

---

## 6. Advanced Features in ProcLens

ProcLens includes advanced, SRE-oriented features to simplify fleet management:

- **Workload Drift Detection**:
  ```bash
  proc-lens drift --file scan.jsonl
  ```
  Analyzes historical logs or stability state files to detect when the workload mix on a node shifts (e.g., if a machine learning training job suddenly appears on a database host).
- **Platform Capability Self-Description**:
  ```bash
  proc-lens capabilities
  ```
  Describes precisely which features are available, degraded, or unavailable on the current platform.
- **Classification Explainability**:
  ```bash
  proc-lens explain --pid <PID>
  ```
  Explains why a given process was classified into a specific archetype, showing heuristic boosters, rule triggers, and what-if scenarios.
- **Resource Pressure and Hardware Topology**:
  ```bash
  proc-lens scan --loop --enable-psi --enable-hardware-profile
  ```
  Tracks Linux Pressure Stall Information (PSI) and discovers CPU (SIMD support), NUMA layout, block devices, and GPU presence.

---

## 7. Production Readiness & Security Checklist

Before deploying ProcLens to production, verify the following configuration gates:

1. **Security Context**: Ensure the Helm chart values have `privileged: false`, `readOnlyRootFilesystem: true`, and the container only requests `CAP_SYS_PTRACE`.
2. **HTTP Hardening**: Always pass `--http-bearer-token` to secure the metrics endpoint when exposed.
3. **Ingress Control**: Ensure `networkPolicy.enabled: true` is configured in `values.yaml` to restrict metrics access to designated Prometheus namespaces.
4. **Privileged Subcommand Restriction**: Do not run `proc-lens run` or live `proc-lens enrich` with LLM providers inside root containers with host access; the built-in security gates will block execution to prevent potential command execution or data exfiltration. Use local-only Ollama endpoints if enrichment is required in secure sub-segments.

