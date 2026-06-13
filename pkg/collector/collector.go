package collector

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/common"
	"github.com/shirou/gopsutil/v3/process"
)

/*
 * Note: This file contains functions to collect system process stats.
 *
 * Caveat 1: Parsing /proc on Linux or using WMI on Windows requires permissions.
 * Ensure that the binary is run with correct privileges to gather complete stats.
 *
 * Caveat 2: Kubernetes pod metadata mapping walks /var/log/containers log symlinks.
 * In case of containerized execution, kindly mount host /var/log directory to resolve the same.
 */

// Global cache for K8s container metadata mapping, which is refreshed every 30 seconds.
// Note that this mutex is crucial to prevent map corruption during concurrent queries.
var (
	podMetaMap     map[string]K8sPodMeta
	podMetaMapTime time.Time
	podMetaMutex   sync.Mutex
)

// K8sPodMeta holds logical Kubernetes coordinates for a container.
type K8sPodMeta struct {
	PodName       string
	Namespace     string
	ContainerName string
}

// ListProcesses returns a list of all running user-space processes in the system.
// Note that the same filters out kernel threads and low-level system helpers early to reduce resource pressure.
func ListProcesses(ctx context.Context) ([]SimpleProcessInfo, error) {
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list PIDs: %w", err)
	}

	var list []SimpleProcessInfo
	for _, pid := range pids {
		// Skip kernel helper processes by PID on Linux
		if pid <= 2 {
			continue
		}

		p, err := process.NewProcessWithContext(ctx, pid)
		if err != nil {
			continue // Process exited or permission denied
		}

		name, _ := p.NameWithContext(ctx)
		if name == "" {
			continue
		}

		// Skip kernel threads (e.g., [kworker], [migration])
		if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
			continue
		}

		info := SimpleProcessInfo{PID: int(pid), Name: name}

		cmdline, _ := p.CmdlineWithContext(ctx)
		if cmdline == "" {
			cmdline = name
		}
		info.Cmdline = cmdline

		list = append(list, info)
	}

	return list, nil
}

// GetRawStats collects raw telemetry counters for a specific PID.
// Note that the same reuses cached process name and cmdline if provided, in order to avoid duplicate syscalls.
func GetRawStats(ctx context.Context, pid int, cachedName string, cachedCmdline string) (RawStats, error) {
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return RawStats{}, fmt.Errorf("process %d not found: %w", pid, err)
	}

	stats := RawStats{
		PID:        pid,
		SampleTime: time.Now(),
	}

	if cachedName != "" {
		stats.Name = cachedName
	} else {
		name, _ := p.NameWithContext(ctx)
		stats.Name = name
	}

	if cachedCmdline != "" {
		stats.Cmdline = cachedCmdline
	} else {
		cmdline, _ := p.CmdlineWithContext(ctx)
		if cmdline == "" {
			cmdline = stats.Name
		}
		stats.Cmdline = cmdline
	}

	// CPU Times
	if cpuTimes, err := p.TimesWithContext(ctx); err == nil {
		stats.CpuTime = cpuTimes.User + cpuTimes.System
		stats.CpuUserTime = cpuTimes.User
		stats.CpuSystemTime = cpuTimes.System
	}

	// Memory Info
	if memInfo, err := p.MemoryInfoWithContext(ctx); err == nil {
		stats.MemRss = memInfo.RSS
		stats.MemVirt = memInfo.VMS
		stats.MemSwap = memInfo.Swap
	}

	// Read Rss breakdown from /proc status on Linux
	stats.RssAnon, stats.RssFile, stats.RssShmem, stats.MemSwap = parseProcStatusMemory(ctx, pid, stats.MemSwap)

	// Threads
	if threads, err := p.NumThreadsWithContext(ctx); err == nil {
		stats.Threads = int(threads)
	}

	// File Descriptors / Windows Handles
	numFDs, err := p.NumFDsWithContext(ctx)
	if err != nil {
		numFDs = 0
	}
	stats.FdCount = int(numFDs)

	// Network Sockets & FD Categorization
	if runtime.GOOS == "linux" {
		if sockets, pipes, epolls, files, err := parseFdTypesLinux(ctx, pid); err == nil {
			stats.SocketCount = sockets
			stats.FdSockets = sockets
			stats.FdPipes = pipes
			stats.FdEpolls = epolls
			stats.FdFiles = files
		} else {
			// Fallback to gopsutil connections
			if conns, err := p.ConnectionsWithContext(ctx); err == nil {
				stats.SocketCount = len(conns)
			}
		}
	} else {
		if conns, err := p.ConnectionsWithContext(ctx); err == nil {
			stats.SocketCount = len(conns)
		}
	}

	// Disk I/O Counters
	if ioCounters, err := p.IOCountersWithContext(ctx); err == nil {
		stats.IoReadBytes = ioCounters.ReadBytes
		stats.IoWriteBytes = ioCounters.WriteBytes
	}

	// Context Switches
	if ctxSwitches, err := p.NumCtxSwitchesWithContext(ctx); err == nil {
		stats.CtxSwitches = ctxSwitches.Voluntary + ctxSwitches.Involuntary
		stats.CtxSwitchesVol = ctxSwitches.Voluntary
		stats.CtxSwitchesInvol = ctxSwitches.Involuntary
	}

	// Ownership and Scheduling Details
	if nice, err := p.NiceWithContext(ctx); err == nil {
		stats.Nice = nice
	}
	if uids, err := p.UidsWithContext(ctx); err == nil {
		stats.Uids = uids
	}
	if gids, err := p.GidsWithContext(ctx); err == nil {
		stats.Gids = gids
	}

	// Parent process links
	if ppid, err := p.PpidWithContext(ctx); err == nil {
		stats.Ppid = int(ppid)
		stats.Pname = getParentProcessName(ctx, stats.Ppid)
	}

	stats.OomScore, stats.OomScoreAdj = readOomScore(ctx, pid)
	stats.CgroupPath, stats.CgroupVersion, stats.ContainerID, stats.PodUID = parseCgroupDetails(ctx, pid)

	// Resolve K8s namespaces details
	if stats.ContainerID != "" {
		stats.PodName, stats.Namespace, stats.ContainerName = getK8sPodMeta(ctx, stats.ContainerID)
	}

	stats.NetNS, stats.PidNS = readNamespaceInodes(ctx, pid)

	if exe, err := p.ExeWithContext(ctx); err == nil {
		stats.ExePath = exe
	}
	if ct, err := p.CreateTimeWithContext(ctx); err == nil {
		stats.CreateTime = ct
	}

	return stats, nil
}

// MonitorProcess samples a process over the given duration to compute rate-based metrics in a cancellable manner.
func MonitorProcess(ctx context.Context, pid int, duration time.Duration) (ProcessStats, error) {
	s1, err := GetRawStats(ctx, pid, "", "")
	if err != nil {
		return ProcessStats{}, err
	}

	select {
	case <-time.After(duration):
		// Proceed
	case <-ctx.Done():
		return ProcessStats{}, ctx.Err()
	}

	s2, err := GetRawStats(ctx, pid, s1.Name, s1.Cmdline)
	if err != nil {
		return ProcessStats{}, err
	}

	return CalculateRateStats(s1, s2), nil
}

// CalculateRateStats computes rate-based metrics from two raw samples.
func CalculateRateStats(s1, s2 RawStats) ProcessStats {
	dt := s2.SampleTime.Sub(s1.SampleTime).Seconds()
	if dt <= 0 {
		dt = 1.0
	}

	cpuUsage := ((s2.CpuTime - s1.CpuTime) / dt) * 100.0
	cpuUserUsage := ((s2.CpuUserTime - s1.CpuUserTime) / dt) * 100.0
	cpuSystemUsage := ((s2.CpuSystemTime - s1.CpuSystemTime) / dt) * 100.0

	var ioReadSpeed float64
	if s2.IoReadBytes >= s1.IoReadBytes {
		ioReadSpeed = float64(s2.IoReadBytes-s1.IoReadBytes) / dt
	}

	var ioWriteSpeed float64
	if s2.IoWriteBytes >= s1.IoWriteBytes {
		ioWriteSpeed = float64(s2.IoWriteBytes-s1.IoWriteBytes) / dt
	}

	ctxSwitchRate := float64(s2.CtxSwitches-s1.CtxSwitches) / dt
	ctxSwitchVolRate := float64(s2.CtxSwitchesVol-s1.CtxSwitchesVol) / dt
	ctxSwitchInvolRate := float64(s2.CtxSwitchesInvol-s1.CtxSwitchesInvol) / dt

	// Ensure stats don't become negative due to precision issues or counters wrapping/resetting
	if cpuUsage < 0 {
		cpuUsage = 0
	}
	if cpuUserUsage < 0 {
		cpuUserUsage = 0
	}
	if cpuSystemUsage < 0 {
		cpuSystemUsage = 0
	}
	if ctxSwitchRate < 0 {
		ctxSwitchRate = 0
	}
	if ctxSwitchVolRate < 0 {
		ctxSwitchVolRate = 0
	}
	if ctxSwitchInvolRate < 0 {
		ctxSwitchInvolRate = 0
	}

	var age float64
	if s2.CreateTime > 0 {
		age = float64(time.Now().UnixMilli()-s2.CreateTime) / 1000.0
		if age < 0 {
			age = 0
		}
	}

	return ProcessStats{
		PID:                s2.PID,
		Name:               s2.Name,
		Cmdline:            s2.Cmdline,
		CpuUsage:           cpuUsage,
		CpuUserUsage:       cpuUserUsage,
		CpuSystemUsage:      cpuSystemUsage,
		MemRss:             s2.MemRss,
		MemVirt:            s2.MemVirt,
		MemSwap:            s2.MemSwap,
		RssAnon:            s2.RssAnon,
		RssFile:            s2.RssFile,
		RssShmem:           s2.RssShmem,
		Threads:            s2.Threads,
		FdCount:            s2.FdCount,
		FdSockets:          s2.FdSockets,
		FdPipes:            s2.FdPipes,
		FdEpolls:           s2.FdEpolls,
		FdFiles:            s2.FdFiles,
		SocketCount:        s2.SocketCount,
		IoReadSpeed:        ioReadSpeed,
		IoWriteSpeed:       ioWriteSpeed,
		CtxSwitchRate:      ctxSwitchRate,
		CtxSwitchVolRate:   ctxSwitchVolRate,
		CtxSwitchInvolRate: ctxSwitchInvolRate,
		Ppid:               s2.Ppid,
		Pname:              s2.Pname,
		Uids:               s2.Uids,
		Gids:               s2.Gids,
		Nice:               s2.Nice,
		OomScore:           s2.OomScore,
		OomScoreAdj:        s2.OomScoreAdj,
		ContainerID:        s2.ContainerID,
		PodUID:             s2.PodUID,
		PodName:            s2.PodName,
		Namespace:          s2.Namespace,
		ContainerName:      s2.ContainerName,
		CgroupPath:         s2.CgroupPath,
		CgroupVersion:      s2.CgroupVersion,
		NetNS:              s2.NetNS,
		PidNS:              s2.PidNS,
		ExePath:            s2.ExePath,
		AgeSeconds:         age,
		SampleTime:         s2.SampleTime,
	}
}

// getHostProcPath resolves the base directory path of the /proc filesystem.
func getHostProcPath(ctx context.Context) string {
	return GetHostProcPath(ctx)
}

// GetHostProcPath resolves the base directory path of the /proc filesystem.
func GetHostProcPath(ctx context.Context) string {
	if envMap, ok := ctx.Value(common.EnvKey).(common.EnvMap); ok {
		if path, exists := envMap[common.HostProcEnvKey]; exists {
			return path
		}
	}
	if hp := os.Getenv("HOST_PROC"); hp != "" {
		return hp
	}
	return "/proc"
}

// GetHostSysPath resolves the base directory path of the /sys filesystem.
func GetHostSysPath(ctx context.Context) string {
	if envMap, ok := ctx.Value(common.EnvKey).(common.EnvMap); ok {
		if path, exists := envMap[common.HostSysEnvKey]; exists {
			return path
		}
	}
	if hs := os.Getenv("HOST_SYS"); hs != "" {
		return hs
	}
	return "/sys"
}

// countSocketsLinux scans /proc/<pid>/fd/ to count socket symlinks quickly.
func countSocketsLinux(ctx context.Context, pid int) (int, error) {
	hostProc := getHostProcPath(ctx)
	fdPath := filepath.Join(hostProc, fmt.Sprintf("%d", pid), "fd")

	dir, err := os.Open(fdPath)
	if err != nil {
		return 0, err
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return 0, err
	}

	socketCount := 0
	for _, name := range names {
		link, err := os.Readlink(filepath.Join(fdPath, name))
		if err == nil && strings.HasPrefix(link, "socket:") {
			socketCount++
		}
	}
	return socketCount, nil
}

// parseFdTypesLinux categorizes file descriptor targets under /proc/<pid>/fd/.
func parseFdTypesLinux(ctx context.Context, pid int) (sockets int, pipes int, epolls int, files int, err error) {
	hostProc := getHostProcPath(ctx)
	fdPath := filepath.Join(hostProc, fmt.Sprintf("%d", pid), "fd")

	dir, err := os.Open(fdPath)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	for _, name := range names {
		if ctx.Err() != nil {
			return 0, 0, 0, 0, ctx.Err()
		}
		link, err := os.Readlink(filepath.Join(fdPath, name))
		if err != nil {
			continue
		}
		if strings.HasPrefix(link, "socket:") {
			sockets++
		} else if strings.HasPrefix(link, "pipe:") {
			pipes++
		} else if strings.HasPrefix(link, "anon_inode:[eventpoll]") || strings.HasPrefix(link, "anon_inode:[eventfd]") {
			epolls++
		} else if strings.HasPrefix(link, "/") {
			files++
		}
	}
	return sockets, pipes, epolls, files, nil
}

// readOomScore reads the OOM score files for a process on Linux.
func readOomScore(ctx context.Context, pid int) (score int, adj int) {
	if runtime.GOOS != "linux" {
		return 0, 0
	}
	hostProc := getHostProcPath(ctx)

	scoreData, err := readLimitFile(filepath.Join(hostProc, fmt.Sprintf("%d", pid), "oom_score"), 4096)
	if err == nil {
		_, _ = fmt.Sscanf(strings.TrimSpace(string(scoreData)), "%d", &score)
	}

	adjData, err := readLimitFile(filepath.Join(hostProc, fmt.Sprintf("%d", pid), "oom_score_adj"), 4096)
	if err == nil {
		_, _ = fmt.Sscanf(strings.TrimSpace(string(adjData)), "%d", &adj)
	}

	return score, adj
}

// parseCgroupDetails extracts cgroup configurations, ContainerID, and PodUID in a single read.
func parseCgroupDetails(ctx context.Context, pid int) (cgPath string, cgVersion string, containerID string, podUID string) {
	if runtime.GOOS != "linux" {
		return "", "", "", ""
	}
	hostProc := getHostProcPath(ctx)
	cgroupFilePath := filepath.Join(hostProc, fmt.Sprintf("%d", pid), "cgroup")
	data, err := readLimitFile(cgroupFilePath, 65536)
	if err != nil {
		return "", "", "", ""
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if ctx.Err() != nil {
			return "", "", "", ""
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}

		hID := parts[0]
		controllers := parts[1]
		p := parts[2]

		if hID == "0" && controllers == "" {
			cgPath = p
			cgVersion = "v2"
		} else if cgPath == "" {
			cgPath = p
			cgVersion = "v1"
		}

		if strings.Contains(p, "kubepods") {
			subparts := strings.Split(p, "/")
			for _, subpart := range subparts {
				if strings.Contains(subpart, "pod") {
					uid := subpart
					if idx := strings.Index(uid, "pod"); idx != -1 {
						uid = uid[idx+3:]
					}
					if idx := strings.Index(uid, ".slice"); idx != -1 {
						uid = uid[:idx]
					}
					podUID = strings.ReplaceAll(uid, "_", "-")
				}
				if strings.Contains(subpart, "containerd-") || strings.Contains(subpart, "docker-") || strings.Contains(subpart, "crio-") {
					cid := subpart
					for _, prefix := range []string{"cri-containerd-", "docker-", "crio-"} {
						if idx := strings.Index(cid, prefix); idx != -1 {
							cid = cid[idx+len(prefix):]
							break
						}
					}
					if idx := strings.Index(cid, ".scope"); idx != -1 {
						cid = cid[:idx]
					}
					containerID = cid
				}
			}
		}
	}
	return cgPath, cgVersion, containerID, podUID
}

// readNamespaceInodes retrieves network and PID namespace inode descriptors on Linux.
func readNamespaceInodes(ctx context.Context, pid int) (netNS string, pidNS string) {
	if runtime.GOOS != "linux" {
		return "", ""
	}
	hostProc := getHostProcPath(ctx)

	if link, err := os.Readlink(filepath.Join(hostProc, fmt.Sprintf("%d", pid), "ns", "net")); err == nil {
		netNS = link
	}
	if link, err := os.Readlink(filepath.Join(hostProc, fmt.Sprintf("%d", pid), "ns", "pid")); err == nil {
		pidNS = link
	}
	return netNS, pidNS
}

// parseProcStatusMemory extracts advanced memory sub-metrics in bytes from status file.
func parseProcStatusMemory(ctx context.Context, pid int, currentSwap uint64) (rssAnon uint64, rssFile uint64, rssShmem uint64, vmSwap uint64) {
	vmSwap = currentSwap
	if runtime.GOOS != "linux" {
		return 0, 0, 0, vmSwap
	}
	hostProc := getHostProcPath(ctx)
	statusPath := filepath.Join(hostProc, fmt.Sprintf("%d", pid), "status")
	data, err := readLimitFile(statusPath, 65536)
	if err != nil {
		return 0, 0, 0, vmSwap
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if ctx.Err() != nil {
			return 0, 0, 0, vmSwap
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var val uint64
		if strings.HasPrefix(line, "RssAnon:") {
			_, _ = fmt.Sscanf(line, "RssAnon: %d kB", &val)
			rssAnon = val * 1024
		} else if strings.HasPrefix(line, "RssFile:") {
			_, _ = fmt.Sscanf(line, "RssFile: %d kB", &val)
			rssFile = val * 1024
		} else if strings.HasPrefix(line, "RssShmem:") {
			_, _ = fmt.Sscanf(line, "RssShmem: %d kB", &val)
			rssShmem = val * 1024
		} else if strings.HasPrefix(line, "VmSwap:") {
			_, _ = fmt.Sscanf(line, "VmSwap: %d kB", &val)
			vmSwap = val * 1024
		}
	}
	return rssAnon, rssFile, rssShmem, vmSwap
}

// getParentProcessName resolves process command name of parent process PID.
func getParentProcessName(ctx context.Context, ppid int) string {
	if ppid <= 0 {
		return ""
	}
	if runtime.GOOS == "linux" {
		hostProc := getHostProcPath(ctx)
		commPath := filepath.Join(hostProc, fmt.Sprintf("%d", ppid), "comm")
		data, err := readLimitFile(commPath, 4096)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	p, err := process.NewProcessWithContext(ctx, int32(ppid))
	if err == nil {
		name, _ := p.NameWithContext(ctx)
		return name
	}
	return ""
}

// getHostLogContainersPath resolves paths to Docker/Kubernetes container logs symlinks.
func getHostLogContainersPath(ctx context.Context) string {
	if hv := os.Getenv("HOST_VAR"); hv != "" {
		return filepath.Join(hv, "log", "containers")
	}
	if hr := os.Getenv("HOST_ROOT"); hr != "" {
		return filepath.Join(hr, "var", "log", "containers")
	}
	return "/var/log/containers"
}

// scanK8sLogContainers walks /var/log/containers to build a map of ContainerID -> K8sPodMeta.
func scanK8sLogContainers(ctx context.Context) map[string]K8sPodMeta {
	metaMap := make(map[string]K8sPodMeta)
	dirPath := getHostLogContainersPath(ctx)

	dir, err := os.Open(dirPath)
	if err != nil {
		return metaMap
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return metaMap
	}

	for _, name := range names {
		if !strings.HasSuffix(name, ".log") {
			continue
		}

		base := name[:len(name)-4]
		dashIdx := strings.LastIndex(base, "-")
		if dashIdx == -1 {
			continue
		}
		containerID := base[dashIdx+1:]

		left := base[:dashIdx]
		parts := strings.Split(left, "_")
		if len(parts) >= 3 {
			podName := parts[0]
			namespace := parts[1]
			containerName := strings.Join(parts[2:], "_")

			metaMap[containerID] = K8sPodMeta{
				PodName:       podName,
				Namespace:     namespace,
				ContainerName: containerName,
			}
		}
	}
	return metaMap
}

// getK8sPodMeta queries the cached container log mappings.
func getK8sPodMeta(ctx context.Context, containerID string) (podName, namespace, containerName string) {
	podMetaMutex.Lock()
	defer podMetaMutex.Unlock()

	if time.Since(podMetaMapTime) > 30*time.Second {
		podMetaMap = scanK8sLogContainers(ctx)
		podMetaMapTime = time.Now()
	}

	if meta, ok := podMetaMap[containerID]; ok {
		return meta.PodName, meta.Namespace, meta.ContainerName
	}

	if len(containerID) > 12 {
		shortID := containerID[:12]
		for k, v := range podMetaMap {
			if strings.HasPrefix(k, shortID) {
				return v.PodName, v.Namespace, v.ContainerName
			}
		}
	}
	return "", "", ""
}

// readLimitFile reads a file from the host filesystem with a size limit to prevent reading infinitely.
// Note that this is crucial for production stability when reading /proc files.
func readLimitFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(io.LimitReader(f, limit))
}

// CollectConcurrentRawStats gathers raw telemetry counters for a slice of process info concurrently.
// It uses a semaphore to limit concurrent requests to 20 and uses a per-process timeout of 200ms.
// The errRecorder and durationRecorder hooks allow registering telemetry events in the caller.
func CollectConcurrentRawStats(ctx context.Context, procs []SimpleProcessInfo, errRecorder func(string), durationRecorder func(time.Duration)) map[int]RawStats {
	var wg sync.WaitGroup
	var mu sync.Mutex
	statsMap := make(map[int]RawStats)

	// Concurrency limiter channel (semaphore) to prevent file descriptor limit errors.
	// Adjust the limit of 20 as per your host requirements.
	sem := make(chan struct{}, 20)

	for _, p := range procs {
		wg.Add(1)
		go func(p SimpleProcessInfo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			// Reuse the already gathered process name and cmdline with a per-PID read timeout (200ms limit)
			subCtx, subCancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer subCancel()

			start := time.Now()
			stats, err := GetRawStats(subCtx, p.PID, p.Name, p.Cmdline)
			duration := time.Since(start)
			if err == nil {
				mu.Lock()
				statsMap[p.PID] = stats
				mu.Unlock()
				if durationRecorder != nil {
					durationRecorder(duration)
				}
			} else {
				if errRecorder != nil {
					errRecorder("get_raw_stats")
				}
			}
		}(p)
	}

	wg.Wait()
	return statsMap
}

