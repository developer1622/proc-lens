// Package hardware provides hardware topology and capability discovery for proc-lens.
//
// # Overview
//
// This package reads hardware metadata that enriches proc-lens's optimization recommendations.
// It answers questions like:
//   - How many NUMA nodes does this system have?
//   - Does this node have NVMe or rotational storage?
//   - Does the CPU support AVX-512, AVX2, or NEON for AI/SIMD workloads?
//   - What is the CPU model and architecture?
//
// All data is sourced from read-only filesystem paths that are already available
// under the existing host-mount model (/proc/cpuinfo, /sys/devices/system/node, /sys/block).
//
// # Design Principles
//
//   - Read-only, no system calls requiring extra privileges.
//   - Graceful degradation: every field that cannot be read returns "no data" with a clear reason.
//   - No mandatory external binaries (lscpu, lsblk, etc.). If they are present on the host PATH
//     and the operator enables --enable-hardware-profile, they are used for richer data. Otherwise,
//     pure /proc and /sys reads are used as the primary source.
//   - The HardwareProfile struct is JSON-serialisable for JSONL pipelines and Prometheus labels.
//
// Note: Hardware topology data changes very rarely (typically only across reboots or
// hot-plug events). Collecting it once per agent startup (or once per scan in one-shot mode)
// is sufficient. The overhead is negligible.
//
// Caveat: In virtualised or cloud environments, many hardware details are synthetic or hidden.
// For example, NUMA nodes may be reported as 1 even on multi-socket hosts if the hypervisor
// does not expose NUMA topology. Storage types may appear as virtio-blk regardless of the
// underlying media. Kindly interpret hardware data with this context in mind.
package hardware

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// StorageType describes the detected type of the primary storage device.
type StorageType string

const (
	StorageTypeNVMe       StorageType = "nvme"
	StorageTypeHDD        StorageType = "hdd"
	StorageTypeSSD        StorageType = "ssd"
	StorageTypeVirtual    StorageType = "virtual"
	StorageTypeUnknown    StorageType = "unknown"
	StorageTypeNoData     StorageType = "no_data"
)

// DataStatus describes the collection status for a hardware data field.
type DataStatus string

const (
	StatusFull     DataStatus = "full"
	StatusPartial  DataStatus = "partial"
	StatusNoData   DataStatus = "no_data"
	StatusDisabled DataStatus = "disabled"
)

// CPUCapabilities represents the ISA extensions detected on the host CPU.
// These are relevant for AI/ML workload recommendations.
type CPUCapabilities struct {
	// Architecture is "amd64", "arm64", etc.
	Architecture string `json:"architecture"`

	// ModelName is the human-readable CPU model string from /proc/cpuinfo.
	ModelName string `json:"model_name"`

	// PhysicalCores is the number of physical CPU cores.
	PhysicalCores int `json:"physical_cores"`

	// LogicalCores is the number of logical CPUs (including hyperthreading).
	LogicalCores int `json:"logical_cores"`

	// HasAVX512 indicates AVX-512 support (Intel Skylake SP+, AMD Zen 4+).
	HasAVX512 bool `json:"has_avx512"`

	// HasAVX2 indicates AVX2 support (Intel Haswell+, AMD Ryzen+).
	HasAVX2 bool `json:"has_avx2"`

	// HasSSE4 indicates SSE4.2 support.
	HasSSE4 bool `json:"has_sse4"`

	// HasNEON indicates ARM NEON SIMD support (arm64 standard).
	HasNEON bool `json:"has_neon"`

	// HasSVE indicates ARM SVE (Scalable Vector Extension) support.
	HasSVE bool `json:"has_sve"`

	// HasAMDGPU indicates AMD GPU presence (detected via /sys/bus/pci).
	HasAMDGPU bool `json:"has_amdgpu"`

	// HasNVIDIAGPU indicates NVIDIA GPU presence (detected via /sys/bus/pci).
	HasNVIDIAGPU bool `json:"has_nvidia_gpu"`

	// Status is "full", "partial", or "no_data".
	Status DataStatus `json:"status"`

	// Reason explains any limitations in the data collected.
	Reason string `json:"reason,omitempty"`
}

// NUMATopology describes the NUMA layout of the host node.
type NUMATopology struct {
	// NodeCount is the number of NUMA nodes detected.
	NodeCount int `json:"node_count"`

	// IsNUMAAvailable indicates whether the /sys/devices/system/node path was readable.
	IsNUMAAvailable bool `json:"is_numa_available"`

	// IsUMA is true when there is only one NUMA node (Uniform Memory Access).
	IsUMA bool `json:"is_uma"`

	// Status is "full", "partial", or "no_data".
	Status DataStatus `json:"status"`

	// Reason explains any limitations.
	Reason string `json:"reason,omitempty"`
}

// StorageInfo describes the primary block devices visible to the system.
type StorageInfo struct {
	// Devices is a list of detected block devices with their characteristics.
	Devices []BlockDevice `json:"devices"`

	// PrimaryType is the storage type of the largest/first detected device.
	PrimaryType StorageType `json:"primary_type"`

	// Status is "full", "partial", or "no_data".
	Status DataStatus `json:"status"`

	// Reason explains any limitations.
	Reason string `json:"reason,omitempty"`
}

// BlockDevice describes a single block storage device.
type BlockDevice struct {
	// Name is the device name (e.g., "sda", "nvme0n1").
	Name string `json:"name"`

	// Type is "nvme", "hdd", "ssd", "virtual", or "unknown".
	Type StorageType `json:"type"`

	// Rotational is true for spinning disk (HDD), false for SSD/NVMe.
	Rotational bool `json:"rotational"`

	// IOScheduler is the I/O scheduler name (e.g., "none", "mq-deadline", "cfq").
	IOScheduler string `json:"io_scheduler"`

	// QueueDepth is the device's hardware queue depth.
	QueueDepth int `json:"queue_depth"`
}

// HardwareProfile is the complete hardware topology report for a node.
// Note: This struct is always safe to embed in JSON output even when fields
// are partially populated or contain "no_data" status entries.
type HardwareProfile struct {
	// Status is the overall collection status: "full", "partial", "no_data", or "disabled".
	Status DataStatus `json:"status"`

	// Reason is set when Status is not "full".
	Reason string `json:"reason,omitempty"`

	// CPU describes the processor capabilities and SIMD extensions.
	CPU CPUCapabilities `json:"cpu"`

	// NUMA describes the memory access topology.
	NUMA NUMATopology `json:"numa"`

	// Storage describes the block storage devices.
	Storage StorageInfo `json:"storage"`

	// RecommendationHints is a list of hardware-aware tuning suggestions generated
	// from the collected topology data. These supplement the optimizer's recommendations.
	RecommendationHints []string `json:"recommendation_hints"`
}

// Collect reads hardware topology from /proc and /sys.
// hostSysPath should be the resolved HOST_SYS path (defaults to "/sys").
// hostProcPath should be the resolved HOST_PROC path (defaults to "/proc").
//
// Note: This function never panics. All errors result in graceful partial/no_data responses.
func Collect(hostProcPath, hostSysPath string) HardwareProfile {
	if hostProcPath == "" {
		hostProcPath = "/proc"
	}
	if hostSysPath == "" {
		hostSysPath = "/sys"
	}

	profile := HardwareProfile{}

	// --- CPU Capabilities ---
	profile.CPU = collectCPU(hostProcPath, hostSysPath)

	// --- NUMA Topology ---
	profile.NUMA = collectNUMA(hostSysPath)

	// --- Storage Info ---
	profile.Storage = collectStorage(hostSysPath)

	// --- Determine overall status ---
	statuses := []DataStatus{profile.CPU.Status, profile.NUMA.Status, profile.Storage.Status}
	fullCount := 0
	noDataCount := 0
	for _, s := range statuses {
		if s == StatusFull {
			fullCount++
		}
		if s == StatusNoData {
			noDataCount++
		}
	}

	switch {
	case fullCount == len(statuses):
		profile.Status = StatusFull
	case noDataCount == len(statuses):
		profile.Status = StatusNoData
		profile.Reason = "No hardware topology data could be collected. This is expected on non-Linux platforms or containers without /sys mounted. All other proc-lens features remain fully functional."
	default:
		profile.Status = StatusPartial
		profile.Reason = "Some hardware topology data could not be collected (see individual field reasons)."
	}

	// --- Generate hardware-aware recommendation hints ---
	profile.RecommendationHints = generateHints(profile)

	return profile
}

// collectCPU reads CPU capability data from /proc/cpuinfo and /sys.
func collectCPU(hostProcPath, hostSysPath string) CPUCapabilities {
	cpu := CPUCapabilities{
		Architecture: runtime.GOARCH,
		LogicalCores: runtime.NumCPU(),
	}

	// On non-Linux, we can only provide basic info from the runtime.
	if runtime.GOOS != "linux" {
		cpu.Status = StatusNoData
		cpu.Reason = fmt.Sprintf("Detailed CPU capability detection requires Linux /proc/cpuinfo. Current OS: %s. Architecture and logical core count are still available from the Go runtime.", runtime.GOOS)
		return cpu
	}

	cpuInfoPath := filepath.Join(hostProcPath, "cpuinfo")
	f, err := os.Open(cpuInfoPath)
	if err != nil {
		cpu.Status = StatusNoData
		cpu.Reason = fmt.Sprintf("Cannot read %s: %v. This is expected in minimal containers without /proc mounted.", cpuInfoPath, err)
		return cpu
	}
	defer f.Close()

	var physicalIDs []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "model name":
			if cpu.ModelName == "" {
				cpu.ModelName = val
			}
		case "flags", "Features":
			// Parse SIMD capabilities from CPU flags.
			flags := strings.Fields(strings.ToLower(val))
			flagSet := make(map[string]bool, len(flags))
			for _, f := range flags {
				flagSet[f] = true
			}
			// x86/amd64 flags
			if flagSet["avx512f"] || flagSet["avx512bw"] || flagSet["avx512vl"] {
				cpu.HasAVX512 = true
			}
			if flagSet["avx2"] {
				cpu.HasAVX2 = true
			}
			if flagSet["sse4_2"] || flagSet["sse4_1"] {
				cpu.HasSSE4 = true
			}
			// ARM flags
			if flagSet["neon"] || flagSet["asimd"] || flagSet["asimdrdm"] {
				cpu.HasNEON = true
			}
			if flagSet["sve"] || flagSet["sve2"] {
				cpu.HasSVE = true
			}
		case "physical id":
			// Count unique physical IDs for physical core estimation.
			found := false
			for _, id := range physicalIDs {
				if id == val {
					found = true
					break
				}
			}
			if !found {
				physicalIDs = append(physicalIDs, val)
			}
		}
	}

	if len(physicalIDs) > 0 {
		// Rough physical core estimate: logical / physical_socket_count
		cpu.PhysicalCores = cpu.LogicalCores / len(physicalIDs)
	} else {
		cpu.PhysicalCores = cpu.LogicalCores
	}

	// Detect GPU presence via /sys/bus/pci/devices.
	cpu.HasNVIDIAGPU, cpu.HasAMDGPU = detectGPU(hostSysPath)

	cpu.Status = StatusFull
	if cpu.ModelName == "" {
		cpu.Status = StatusPartial
		cpu.Reason = "CPU model name not found in /proc/cpuinfo (may be a virtualised environment)."
	}

	return cpu
}

// detectGPU checks /sys/bus/pci/devices for known GPU vendor IDs.
// Returns (hasNVIDIA, hasAMD). Both may be false in containers without /sys mounted.
func detectGPU(hostSysPath string) (bool, bool) {
	pciPath := filepath.Join(hostSysPath, "bus", "pci", "devices")
	entries, err := os.ReadDir(pciPath)
	if err != nil {
		return false, false
	}

	var hasNVIDIA, hasAMD bool
	for _, e := range entries {
		vendorPath := filepath.Join(pciPath, e.Name(), "vendor")
		data, err := os.ReadFile(vendorPath)
		if err != nil {
			continue
		}
		vendor := strings.TrimSpace(string(data))
		switch vendor {
		case "0x10de": // NVIDIA
			hasNVIDIA = true
		case "0x1002": // AMD
			hasAMD = true
		}
		if hasNVIDIA && hasAMD {
			break
		}
	}
	return hasNVIDIA, hasAMD
}

// collectNUMA reads NUMA topology from /sys/devices/system/node.
func collectNUMA(hostSysPath string) NUMATopology {
	if runtime.GOOS != "linux" {
		return NUMATopology{
			Status: StatusNoData,
			Reason: fmt.Sprintf("NUMA topology requires Linux /sys/devices/system/node. Current OS: %s.", runtime.GOOS),
		}
	}

	nodePath := filepath.Join(hostSysPath, "devices", "system", "node")
	entries, err := os.ReadDir(nodePath)
	if err != nil {
		return NUMATopology{
			Status: StatusNoData,
			Reason: fmt.Sprintf("Cannot read NUMA topology from %s: %v. This is expected in containers without /sys mounted. Kindly mount host /sys for NUMA visibility.", nodePath, err),
		}
	}

	nodeCount := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "node") && e.IsDir() {
			nodeCount++
		}
	}

	if nodeCount == 0 {
		return NUMATopology{
			Status: StatusNoData,
			Reason: fmt.Sprintf("No NUMA node directories found under %s. This is unexpected — the /sys tree may be incomplete.", nodePath),
		}
	}

	return NUMATopology{
		NodeCount:       nodeCount,
		IsNUMAAvailable: true,
		IsUMA:           nodeCount == 1,
		Status:          StatusFull,
	}
}

// collectStorage reads block device information from /sys/block.
func collectStorage(hostSysPath string) StorageInfo {
	if runtime.GOOS != "linux" {
		return StorageInfo{
			PrimaryType: StorageTypeNoData,
			Status:      StatusNoData,
			Reason:      fmt.Sprintf("Storage detection requires Linux /sys/block. Current OS: %s.", runtime.GOOS),
		}
	}

	blockPath := filepath.Join(hostSysPath, "block")
	entries, err := os.ReadDir(blockPath)
	if err != nil {
		return StorageInfo{
			PrimaryType: StorageTypeNoData,
			Status:      StatusNoData,
			Reason:      fmt.Sprintf("Cannot read block device list from %s: %v. Kindly mount host /sys for storage visibility.", blockPath, err),
		}
	}

	var devices []BlockDevice
	for _, e := range entries {
		name := e.Name()
		// Skip RAM disks, loop devices, and dm- devices for clarity.
		if strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "sr") {
			continue
		}

		dev := BlockDevice{Name: name}

		// Detect if rotational (HDD) or not (SSD/NVMe).
		rotPath := filepath.Join(blockPath, name, "queue", "rotational")
		if data, err := os.ReadFile(rotPath); err == nil {
			dev.Rotational = strings.TrimSpace(string(data)) == "1"
		}

		// Detect I/O scheduler.
		schedPath := filepath.Join(blockPath, name, "queue", "scheduler")
		if data, err := os.ReadFile(schedPath); err == nil {
			// The active scheduler is shown in brackets: "mq-deadline [none] kyber"
			sched := strings.TrimSpace(string(data))
			if start := strings.Index(sched, "["); start != -1 {
				if end := strings.Index(sched[start:], "]"); end != -1 {
					dev.IOScheduler = sched[start+1 : start+end]
				}
			}
			if dev.IOScheduler == "" {
				dev.IOScheduler = sched
			}
		}

		// Detect queue depth.
		qdPath := filepath.Join(blockPath, name, "queue", "nr_requests")
		if data, err := os.ReadFile(qdPath); err == nil {
			if qd, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				dev.QueueDepth = qd
			}
		}

		// Classify device type.
		dev.Type = classifyDevice(name, dev.Rotational)
		devices = append(devices, dev)
	}

	if len(devices) == 0 {
		return StorageInfo{
			PrimaryType: StorageTypeNoData,
			Status:      StatusNoData,
			Reason:      "No block devices found under /sys/block (excluding loop, ram, and optical devices). This may be expected in minimal container environments.",
		}
	}

	// Determine primary storage type.
	primaryType := devices[0].Type

	return StorageInfo{
		Devices:     devices,
		PrimaryType: primaryType,
		Status:      StatusFull,
	}
}

// classifyDevice determines the StorageType from the device name and rotational flag.
func classifyDevice(name string, rotational bool) StorageType {
	nameLower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(nameLower, "nvme"):
		return StorageTypeNVMe
	case strings.HasPrefix(nameLower, "vd") || strings.HasPrefix(nameLower, "xvd"):
		return StorageTypeVirtual
	case rotational:
		return StorageTypeHDD
	default:
		return StorageTypeSSD
	}
}

// generateHints creates hardware-aware recommendation hints from the collected profile.
// Note: These hints are additive to the optimizer's main recommendations.
// They reflect hardware-specific best practices that generic workload rules cannot capture.
func generateHints(profile HardwareProfile) []string {
	var hints []string

	// NUMA hints
	if profile.NUMA.IsNUMAAvailable && !profile.NUMA.IsUMA && profile.NUMA.NodeCount > 1 {
		hints = append(hints, fmt.Sprintf(
			"NUMA: This node has %d NUMA domains. For database, AI/ML, and vector search workloads, binding processes to a single NUMA node with 'numactl --cpunodebind=0 --localalloc' can reduce memory latency by 30-60%%. Use 'numastat -p <pid>' to verify.",
			profile.NUMA.NodeCount,
		))
	}

	// GPU hints
	if profile.CPU.HasNVIDIAGPU {
		hints = append(hints, "GPU: NVIDIA GPU detected. For AI training/inference workloads, ensure CUDA drivers and cuDNN are installed and verify GPU utilisation with 'nvidia-smi'. Monitor GPU memory with 'nvidia-smi --query-gpu=memory.used --format=csv'.")
	}
	if profile.CPU.HasAMDGPU {
		hints = append(hints, "GPU: AMD GPU detected. For AI/ML workloads using ROCm, verify the ROCm platform version matches your framework requirements using 'rocm-smi'.")
	}

	// SIMD hints for AI workloads
	if profile.CPU.HasAVX512 {
		hints = append(hints, "SIMD: CPU supports AVX-512. AI/ML frameworks (PyTorch, TensorFlow, ONNX Runtime) can leverage this for significant speedups on CPU inference. Ensure your binary was compiled with '-march=skylake-avx512' or equivalent.")
	} else if profile.CPU.HasAVX2 {
		hints = append(hints, "SIMD: CPU supports AVX2 but not AVX-512. AI workloads will use AVX2 SIMD paths. Consider upgrading to an AVX-512 capable CPU for 2-4x vector compute throughput improvement on AI inference.")
	} else if profile.CPU.HasNEON {
		hints = append(hints, "SIMD: ARM CPU with NEON detected. AI frameworks with ARM-optimised builds (e.g., PyTorch ARM, TFLite, ONNX Runtime ARM) will use NEON for vector compute. Verify with 'cat /proc/cpuinfo | grep neon'.")
	}

	// Storage hints
	if profile.Storage.PrimaryType == StorageTypeHDD {
		hints = append(hints, "Storage: Rotational (HDD) storage detected. For database and event streaming workloads, consider NVMe or SSD for write-heavy workloads. If HDD is required, use the 'deadline' I/O scheduler and tune read-ahead with 'blockdev --setra'.")
	} else if profile.Storage.PrimaryType == StorageTypeNVMe {
		hints = append(hints, "Storage: NVMe storage detected. Ensure the I/O scheduler is set to 'none' (bypasses kernel queuing for maximum IOPS): 'echo none > /sys/block/nvme0n1/queue/scheduler'.")
	}

	if len(hints) == 0 {
		hints = append(hints, "No hardware-specific optimisation hints could be generated from the available topology data.")
	}

	return hints
}

// DisabledProfile returns a HardwareProfile indicating the feature is turned off.
func DisabledProfile() HardwareProfile {
	return HardwareProfile{
		Status: StatusDisabled,
		Reason: "Hardware profile collection is disabled. Enable with --enable-hardware-profile flag.",
		CPU:    CPUCapabilities{Status: StatusDisabled, Architecture: runtime.GOARCH, LogicalCores: runtime.NumCPU()},
		NUMA:   NUMATopology{Status: StatusDisabled},
		Storage: StorageInfo{Status: StatusDisabled, PrimaryType: StorageTypeNoData},
	}
}

