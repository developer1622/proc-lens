package collector

import "time"

/*
 * Note: This file defines the telemetry and context data structures populated by the collector.
 *
 * Caveat: Struct tags here map to standard snake_case JSON schemas for Prometheus and LLM ingestion.
 * Ensure that any modifications to JSON struct tags do not break compatibility with downstream APIs.
 */

// CollectionContext represents the node hardware and OS environment metadata.
// Note that this captures static/semi-static details of the host environment.
type CollectionContext struct {
	NodeName              string      `json:"node_name"`
	KernelVersion         string      `json:"kernel_version"`
	OS                    string      `json:"os"`
	Architecture          string      `json:"architecture"`
	TotalMemoryBytes      uint64      `json:"total_memory_bytes"`
	CpuCores              int         `json:"cpu_cores"`
	Timestamp             time.Time   `json:"timestamp"`
	AgentVersion          string      `json:"agent_version"`
	TotalProcessesScanned int         `json:"total_processes_scanned"`
	Pressure              interface{} `json:"pressure,omitempty"`
	Hardware              interface{} `json:"hardware,omitempty"`
	DataCompleteness      interface{} `json:"data_completeness,omitempty"`
	DataCompletenessScore float64     `json:"data_completeness_score,omitempty"`
}

// RawStats holds raw counters collected at a specific point in time.
type RawStats struct {
	PID              int
	Name             string
	Cmdline          string
	CpuTime          float64   // Total CPU time (utime + stime) in seconds
	CpuUserTime      float64   // User CPU time in seconds
	CpuSystemTime    float64   // System CPU time in seconds
	MemRss           uint64    // Resident Set Size (RSS) in bytes
	MemVirt          uint64    // Virtual memory size in bytes
	MemSwap          uint64    // Swap memory size in bytes
	RssAnon          uint64    // Anonymous RSS memory in bytes (Linux)
	RssFile          uint64    // File-backed RSS memory in bytes (Linux)
	RssShmem         uint64    // Shared RSS memory in bytes (Linux)
	Threads          int       // Thread count
	FdCount          int       // Total open file descriptors
	FdSockets        int       // Socket file descriptors (Linux)
	FdPipes          int       // Pipe file descriptors (Linux)
	FdEpolls         int       // Epoll/Eventfd file descriptors (Linux)
	FdFiles          int       // Regular file descriptors (Linux)
	SocketCount      int       // Open sockets count
	IoReadBytes      uint64    // Cumulative bytes read
	IoWriteBytes     uint64    // Cumulative bytes written
	CtxSwitches      int64     // Total context switches
	CtxSwitchesVol   int64     // Voluntary context switches
	CtxSwitchesInvol int64     // Involuntary context switches
	Ppid             int       // Parent Process ID
	Pname            string    // Parent Process Name
	Uids             []int32   // Process UIDs [real, effective, saved, filesystem]
	Gids             []int32   // Process GIDs [real, effective, saved, filesystem]
	Nice             int32     // Scheduling nice value
	OomScore         int       // OOM score on Linux
	OomScoreAdj      int       // OOM score adjustment on Linux
	ContainerID      string    // Container ID parsed from cgroups
	PodUID           string    // Pod UID parsed from cgroups
	PodName          string    // Kubernetes Pod Name resolved via container mapping
	Namespace        string    // Kubernetes Namespace resolved via container mapping
	ContainerName    string    // Kubernetes Container Name resolved via container mapping
	CgroupPath       string    // Process control group path
	CgroupVersion    string    // Process control group version (v1 or v2)
	NetNS            string    // Network namespace identifier
	PidNS            string    // PID namespace identifier
	ExePath          string    // Resolved absolute executable path
	CreateTime       int64     // Creation time in milliseconds since epoch
	SampleTime       time.Time // Collection timestamp
}

// ProcessStats holds calculated rates and instantaneous stats of a process.
type ProcessStats struct {
	PID                int       `json:"pid"`
	Name               string    `json:"name"`
	Cmdline            string    `json:"cmdline"`
	CpuUsage           float64   `json:"cpu_usage"`             // CPU utilization percentage
	CpuUserUsage       float64   `json:"cpu_user_usage"`        // User CPU utilization percentage
	CpuSystemUsage      float64   `json:"cpu_system_usage"`       // System CPU utilization percentage
	MemRss             uint64    `json:"mem_rss"`               // RSS memory in bytes
	MemVirt            uint64    `json:"mem_virt"`              // Virtual memory in bytes
	MemSwap            uint64    `json:"mem_swap"`              // Swap memory in bytes
	RssAnon            uint64    `json:"rss_anon"`              // Anonymous RSS memory in bytes (Linux)
	RssFile            uint64    `json:"rss_file"`              // File-backed RSS memory in bytes (Linux)
	RssShmem           uint64    `json:"rss_shmem"`             // Shared RSS memory in bytes (Linux)
	Threads            int       `json:"threads"`               // Number of threads
	FdCount            int       `json:"fd_count"`              // Total open file descriptors
	FdSockets          int       `json:"fd_sockets"`            // Socket file descriptors
	FdPipes            int       `json:"fd_pipes"`              // Pipe file descriptors
	FdEpolls           int       `json:"fd_epolls"`             // Epoll/Eventfd file descriptors
	FdFiles            int       `json:"fd_files"`              // Regular file descriptors
	SocketCount        int       `json:"socket_count"`          // Number of open network sockets
	IoReadSpeed        float64   `json:"io_read_speed"`         // Read speed in bytes/sec
	IoWriteSpeed       float64   `json:"io_write_speed"`        // Write speed in bytes/sec
	CtxSwitchRate      float64   `json:"ctx_switch_rate"`       // Total context switches per second
	CtxSwitchVolRate   float64   `json:"ctx_switch_vol_rate"`   // Voluntary context switches per second
	CtxSwitchInvolRate float64   `json:"ctx_switch_invol_rate"` // Involuntary context switches per second
	Ppid               int       `json:"ppid"`                  // Parent PID
	Pname              string    `json:"pname"`                 // Parent Name
	Uids               []int32   `json:"uids"`                  // Process UIDs
	Gids               []int32   `json:"gids"`                  // Process GIDs
	Nice               int32     `json:"nice"`                  // Nice value
	OomScore           int       `json:"oom_score"`             // OOM score
	OomScoreAdj        int       `json:"oom_score_adj"`         // OOM score adj
	ContainerID        string    `json:"container_id"`          // Container ID
	PodUID             string    `json:"pod_uid"`               // Pod UID
	PodName            string    `json:"pod_name"`              // Pod Name
	Namespace          string    `json:"namespace"`             // Pod Namespace
	ContainerName      string    `json:"container_name"`        // Container Name
	CgroupPath         string    `json:"cgroup_path"`           // Cgroup Path
	CgroupVersion      string    `json:"cgroup_version"`        // Cgroup Version
	NetNS              string    `json:"net_ns"`                // Network Namespace
	PidNS              string    `json:"pid_ns"`                // PID Namespace
	ExePath            string    `json:"exe_path"`              // Resolved executable path
	AgeSeconds         float64   `json:"age_seconds"`           // Process age in seconds
	SampleTime         time.Time `json:"sample_time"`           // Time of calculation
}

// SimpleProcessInfo holds minimal static information about a running process.
type SimpleProcessInfo struct {
	PID     int
	Name    string
	Cmdline string
}

