package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/metrics"
	"github.com/developer1622/proc-lens/pkg/optimizer"

	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/spf13/cobra"
)

/*
 * Note: This file contains the enrich command which integrates with LLM APIs to perform advanced analysis.
 *
 * Caveat 1: Sending telemetry data to external third-party APIs may present data exfiltration risks.
 * Pass --allow-remote-llm to explicitly consent to sending this telemetry outside your host.
 *
 * Caveat 2: Large numbers of processes can inflate LLM prompt token counts. By default, we trim the payload
 * to the top N resource-consuming processes (controlled by the --top flag). Kindly review this limit.
 */

type EnrichOptions struct {
	Provider      string
	Model         string
	Endpoint      string
	File          string
	Pid           int
	TopCount      int
	AllowRemote   bool
	Duration      time.Duration
	OutputFormat  string
	ExposeCmdline bool
}

var enrichOpts EnrichOptions

// EnvelopedPredictions represents the fully-packaged payload structure for prompt injection.
type EnvelopedPredictions struct {
	Context     collector.CollectionContext `json:"collection_context"`
	Predictions []classifier.Prediction     `json:"predictions"`
}

var enrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "Enrich process telemetry using frontier LLM reasoning",
	Long: `Packages local process telemetry along with node metadata, 
and queries LLM APIs (Gemini, Claude, OpenAI, Grok, or local Ollama) 
for SRE workload intent analyses and prioritized optimization advice.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(GetHostContext())
		defer cancel()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigChan
			cancel()
		}()

		opts := enrichOpts
		opts.OutputFormat = GlobalOpts.OutputFormat
		opts.ExposeCmdline = GlobalOpts.ExposeCmdline
		return RunEnrich(ctx, &opts)
	},
}

func RunEnrich(ctx context.Context, opts *EnrichOptions) error {
	// Validate profiling duration
	if opts.Duration <= 0 {
		err := fmt.Errorf("invalid profiling duration: %v. Provide a positive profiling duration", opts.Duration)
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	// Security Gating in host-privileged context (Concern 4)
	if hp := os.Getenv("HOST_PROC"); hp != "" && os.Getuid() == 0 && opts.AllowRemote && opts.File == "" {
		err := fmt.Errorf("security gate blocked execution: refusing to transmit live telemetry to remote LLMs from a host-privileged context (UID 0 and HOST_PROC active). Use a non-privileged binary instead")
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sSecurity Error: %v%s\n", Red, err, Reset)
		}
		return err
	}

	// Check if running in a container with host PID access
	if hp := os.Getenv("HOST_PROC"); hp != "" {
		slog.Warn("enrich command is running inside a container with host PID access. Ensure outbound firewall rules are strictly configured to prevent data exfiltration.")
	}

	// Security Warning for Privilege Context (Concern 5)
	if !opts.AllowRemote && opts.File == "" {
		if os.Getuid() == 0 {
			slog.Warn("proc-lens is running as UID 0 (root). Access to host processes is highly privileged. Ensure remote LLM communication is secure and authorized.")
		}
	} else if opts.AllowRemote {
		if os.Getuid() == 0 {
			slog.Warn("WARNING: proc-lens is running as UID 0 (root) and --allow-remote-llm is active. Telemetry collected under root context will be sent to external LLMs. Ensure absolute security of the execution node.")
		}
	}

	// 1. Resolve Provider Configurations
	provider := strings.ToLower(opts.Provider)
	apiKey := resolveApiKey(provider)
	endpoint := opts.Endpoint

	// Remote LLM safety verification
	isLocal := false
	if endpoint != "" && (strings.Contains(endpoint, "localhost") || strings.Contains(endpoint, "127.0.0.1")) {
		isLocal = true
	}
	if !isLocal && !opts.AllowRemote {
		err := fmt.Errorf("security validation failed: sending node process intelligence telemetry to external LLM APIs poses data exfiltration risks. Pass --allow-remote-llm explicitly to proceed with the execution")
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sSecurity Warning: %v%s\n", Red, err, Reset)
		}
		return err
	}

	// If no key is set and we're not hitting a custom/local endpoint, fail early
	if apiKey == "" && endpoint == "" {
		err := fmt.Errorf("API Key is required for provider '%s'. Set the corresponding environment variable (e.g. GEMINI_API_KEY, OPENAI_API_KEY, GROK_API_KEY, or ANTHROPIC_API_KEY) and try again", provider)
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	// 2. Fetch and package process predictions
	var payload EnvelopedPredictions

	if opts.File != "" {
		// Load from exported JSON file
		if opts.OutputFormat != "json" {
			fmt.Printf("Loading process telemetry payload from file: %s...\n", opts.File)
		}
		var err error
		payload, err = loadPayloadFromFile(opts.File)
		if err != nil {
			slog.Error("Failed to parse input file", "error", err)
			return err
		}
	} else {
		// Profile live system
		if opts.OutputFormat != "json" {
			fmt.Printf("Gathering live node process telemetry (profiling duration: %v)...\n", opts.Duration)
		}
		var err error
		payload, err = captureLivePayload(ctx, opts)
		if err != nil {
			slog.Error("Failed to capture live telemetry", "error", err)
			return err
		}
	}

	// Filter to specific PID if requested
	if opts.Pid > 0 {
		var filtered []classifier.Prediction
		for _, p := range payload.Predictions {
			if p.PID == opts.Pid {
				filtered = append(filtered, p)
				break
			}
		}
		payload.Predictions = filtered
	}

	// Trim predictions to top-N limits to conserve token counts
	if len(payload.Predictions) > opts.TopCount {
		payload.Predictions = payload.Predictions[:opts.TopCount]
	}

	if len(payload.Predictions) == 0 {
		fmt.Println("No active processes matching the specified thresholds were found.")
		return nil
	}

	// If format is json, we print the raw enveloped payload and exit without calling LLM
	if opts.OutputFormat == "json" {
		bz, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return err
		}
		fmt.Println(string(bz))
		return nil
	}

	// 3. Construct prompt templates
	systemPrompt := `You are a 20-year veteran Linux kernel SRE and systems architect who has managed large-scale Kubernetes clusters and high-performance database infrastructure. 
You are given a JSON payload containing process resource telemetry (CPU user/system splits, memory RSS/VMS/swap, thread counts, open FDs, socket metrics, voluntary/involuntary context switches, container/cgroup scopes, and absolute binary paths) along with host node metadata.
Your task is to analyze this data and generate a narrative, context-aware systems analysis report.
Do not hallucinate parameters. All suggestions must be grounded strictly in the provided metrics.
Separate container-level settings from host-level global sysctls explicitly. Flag any optimization that requires host root privileges.`

	var processDetails []string
	for _, p := range payload.Predictions {
		memMb := float64(p.Telemetry.MemRss) / (1024 * 1024)
		swapMb := float64(p.Telemetry.MemSwap) / (1024 * 1024)
		pStr := fmt.Sprintf(`- PID %d (%s) [Archetype: %s (Confidence: %.1f%%)]:
  Cmdline: %s
  Executable: %s
  Container ID: %s | Pod UID: %s
  Nice: %d | OOM Score: %d (Adj: %d)
  CPU Usage: %.1f%% (User: %.1f%%, System: %.1f%%)
  Memory RSS: %.1f MB | Swap: %.1f MB | Threads: %d
  Open FDs: %d | Sockets: %d
  Disk Read Speed: %.1f B/s | Write Speed: %.1f B/s
  Context Switches: %.1f/s (Voluntary: %.1f/s, Involuntary: %.1f/s)
  Local Recommendations: %s`,
			p.PID, p.Name, p.PrimaryCategory, p.Confidence*100.0,
			p.Cmdline, p.Telemetry.ExePath, p.Telemetry.ContainerID, p.Telemetry.PodUID,
			p.Telemetry.Nice, p.Telemetry.OomScore, p.Telemetry.OomScoreAdj,
			p.Telemetry.CpuUsage, p.Telemetry.CpuUserUsage, p.Telemetry.CpuSystemUsage,
			memMb, swapMb, p.Telemetry.Threads,
			p.Telemetry.FdCount, p.Telemetry.SocketCount,
			p.Telemetry.IoReadSpeed, p.Telemetry.IoWriteSpeed,
			p.Telemetry.CtxSwitchRate, p.Telemetry.CtxSwitchVolRate, p.Telemetry.CtxSwitchInvolRate,
			strings.Join(p.Recommendations, "; "),
		)
		processDetails = append(processDetails, pStr)
	}

	userPrompt := fmt.Sprintf(`Here is the host node system context:
- Hostname: %s
- OS/Arch: %s/%s
- Kernel Version: %s
- CPU Cores: %d
- Total RAM: %.1f GB
- Timestamp: %s
- Total Processes Scanned: %d
 
Here are the top %d profiled process predictions on this node:
%s

Generate an SRE report containing:
1. Executive Summary: A concise one-paragraph summary of node workload health.
2. Workload Origin & Intent: Interpret the purpose of these processes based on command lines and resource footprints.
3. System Risk Analysis: Identify bottlenecks (e.g. CPU scheduler contention via involuntary context switches, OOM scores, memory leaks, I/O queues).
4. Prioritized Optimization Plan: Actionable kernel parameter optimizations. For each, list:
   - The sysctl/ulimit command
   - Rationale and impact
   - Verification command
   - Risk classification (Low/Medium/High)`,
		payload.Context.NodeName, payload.Context.OS, payload.Context.Architecture,
		payload.Context.KernelVersion, payload.Context.CpuCores,
		float64(payload.Context.TotalMemoryBytes)/(1024*1024*1024),
		payload.Context.Timestamp.Format(time.RFC3339),
		payload.Context.TotalProcessesScanned,
		len(payload.Predictions),
		strings.Join(processDetails, "\n\n"),
	)

	// 4. Query the LLM API with a simple loading spinner
	fmt.Printf("Kindly wait, querying %s (%s) for SRE analysis... ", provider, modelWithDefault(provider, opts.Model))
	done := make(chan bool)
	go func() {
		chars := []string{"|", "/", "-", "\\"}
		i := 0
		for {
			select {
			case <-done:
				return
			default:
				fmt.Printf("\rKindly wait, querying %s (%s) for SRE analysis... %s", provider, modelWithDefault(provider, opts.Model), chars[i])
				i = (i + 1) % len(chars)
				time.Sleep(150 * time.Millisecond)
			}
		}
	}()

	response, err := callLLMAPI(ctx, provider, apiKey, modelWithDefault(provider, opts.Model), endpoint, systemPrompt, userPrompt)
	done <- true
	fmt.Print("\r") // Clear spinner line

	if err != nil {
		fmt.Printf("%sError calling LLM API: %v. check credentials/network connection.%s\n", Red, err, Reset)
		return err
	}

	// 5. Print the rich markdown response
	fmt.Println()
	fmt.Println(response)
	return nil
}

func init() {
	enrichCmd.Flags().StringVar(&enrichOpts.Provider, "provider", "gemini", "LLM API provider (gemini, openai, grok, claude)")
	enrichCmd.Flags().StringVar(&enrichOpts.Model, "model", "", "Model name (defaults: gemini-2.5-flash, gpt-4o-mini, grok-2, claude-3-5-sonnet)")
	enrichCmd.Flags().StringVar(&enrichOpts.Endpoint, "endpoint", "", "Custom HTTP URL (e.g. http://localhost:11434/v1/chat/completions for Ollama)")
	enrichCmd.Flags().StringVarP(&enrichOpts.File, "file", "f", "", "Load process JSON payload from file instead of running live profiling")
	enrichCmd.Flags().IntVarP(&enrichOpts.Pid, "pid", "p", 0, "Limit enrichment analysis to a specific process PID")
	enrichCmd.Flags().IntVar(&enrichOpts.TopCount, "top", 5, "Limit analysis to the top N resource consumers")
	enrichCmd.Flags().DurationVarP(&enrichOpts.Duration, "duration", "d", 1*time.Second, "Profiling window duration for live scan (e.g. 1s, 2s)")
	enrichCmd.Flags().BoolVar(&enrichOpts.AllowRemote, "allow-remote-llm", false, "Explicitly allow sending node process intelligence telemetry to external LLM APIs (WARNING: security risk)")

	RootCmd.AddCommand(enrichCmd)
}

func resolveApiKey(provider string) string {
	if genericKey := os.Getenv("LLM_API_KEY"); genericKey != "" {
		return genericKey
	}
	switch provider {
	case "gemini":
		return os.Getenv("GEMINI_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "grok":
		return os.Getenv("GROK_API_KEY")
	case "claude":
		return os.Getenv("ANTHROPIC_API_KEY")
	}
	return ""
}

func modelWithDefault(provider, userModel string) string {
	if userModel != "" {
		return userModel
	}
	switch provider {
	case "gemini":
		return "gemini-2.5-flash"
	case "openai":
		return "gpt-4o-mini"
	case "grok":
		return "grok-2"
	case "claude":
		return "claude-3-5-sonnet-20241022"
	}
	return "default"
}

func loadPayloadFromFile(path string) (EnvelopedPredictions, error) {
	bz, err := os.ReadFile(path)
	if err != nil {
		return EnvelopedPredictions{}, err
	}

	// Try reading enveloped format first
	var payload EnvelopedPredictions
	if err := json.Unmarshal(bz, &payload); err == nil && len(payload.Predictions) > 0 {
		return payload, nil
	}

	// Fallback to array format
	var array []classifier.Prediction
	if err := json.Unmarshal(bz, &array); err == nil {
		// Populate dummy context
		return EnvelopedPredictions{
			Context: collector.CollectionContext{
				NodeName:     "imported-node",
				OS:           runtime.GOOS,
				Architecture: runtime.GOARCH,
				Timestamp:    time.Now(),
				AgentVersion: Version,
			},
			Predictions: array,
		}, nil
	}

	return EnvelopedPredictions{}, fmt.Errorf("file format is not recognized as valid predictions array or enveloped payload")
}

func captureLivePayload(ctx context.Context, opts *EnrichOptions) (EnvelopedPredictions, error) {
	procs, err := collector.ListProcesses(ctx)
	if err != nil {
		return EnvelopedPredictions{}, err
	}

	// Helper to get a timeout context for the collection.
	getTimeoutCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 5*time.Second)
	}

	c1Ctx, c1Cancel := getTimeoutCtx()
	s1Map := collector.CollectConcurrentRawStats(c1Ctx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
	c1Cancel()
	select {
	case <-time.After(opts.Duration):
		// Proceed
	case <-ctx.Done():
		return EnvelopedPredictions{}, ctx.Err()
	}
	c2Ctx, c2Cancel := getTimeoutCtx()
	s2Map := collector.CollectConcurrentRawStats(c2Ctx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
	c2Cancel()

	var predictions []classifier.Prediction
	for _, p := range procs {
		s1, exists1 := s1Map[p.PID]
		s2, exists2 := s2Map[p.PID]
		if !exists1 || !exists2 {
			continue
		}

		stats := collector.CalculateRateStats(s1, s2)
		pred := classifier.Predict(stats)
		pred.Cmdline = RedactCmdline(pred.Cmdline, opts.ExposeCmdline)
		pred.Telemetry.Cmdline = RedactCmdline(pred.Telemetry.Cmdline, opts.ExposeCmdline)
		optimizer.Optimize(ctx, &pred)
		predictions = append(predictions, pred)
	}

	// Sort by CPU usage descending
	sort.Slice(predictions, func(i, j int) bool {
		return predictions[i].Telemetry.CpuUsage > predictions[j].Telemetry.CpuUsage
	})

	// Collect System hardware properties
	hostname, _ := os.Hostname()
	var kernelVersion string
	if hostInfo, err := host.InfoWithContext(ctx); err == nil {
		kernelVersion = hostInfo.KernelVersion
	}
	var totalMemory uint64
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		totalMemory = v.Total
	}

	return EnvelopedPredictions{
		Context: collector.CollectionContext{
			NodeName:              hostname,
			KernelVersion:         kernelVersion,
			OS:                    runtime.GOOS,
			Architecture:          runtime.GOARCH,
			TotalMemoryBytes:      totalMemory,
			CpuCores:              runtime.NumCPU(),
			Timestamp:             time.Now(),
			AgentVersion:          Version,
			TotalProcessesScanned: len(procs),
		},
		Predictions: predictions,
	}, nil
}

func callLLMAPI(ctx context.Context, provider, apiKey, model, endpoint, systemPrompt, userPrompt string) (string, error) {
	switch provider {
	case "gemini":
		return callGemini(ctx, apiKey, model, userPrompt, systemPrompt)
	case "openai", "grok":
		ep := endpoint
		if ep == "" {
			if provider == "grok" {
				ep = "https://api.x.ai/v1/chat/completions"
			} else {
				ep = "https://api.openai.com/v1/chat/completions"
			}
		}
		return callOpenAICompatible(ctx, apiKey, model, ep, userPrompt, systemPrompt)
	case "claude":
		return callClaude(ctx, apiKey, model, userPrompt, systemPrompt)
	default:
		// Attempt OpenAI-compatible format if custom endpoint is set
		if endpoint != "" {
			return callOpenAICompatible(ctx, apiKey, model, endpoint, userPrompt, systemPrompt)
		}
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func callGemini(ctx context.Context, apiKey, model, userPrompt, systemPrompt string) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)

	reqPayload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": userPrompt},
				},
			},
		},
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": systemPrompt},
			},
		},
	}

	bz, _ := json.Marshal(reqPayload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var res struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(respBody, &res); err != nil {
		return "", err
	}

	if len(res.Candidates) > 0 && len(res.Candidates[0].Content.Parts) > 0 {
		return res.Candidates[0].Content.Parts[0].Text, nil
	}

	return "", fmt.Errorf("empty response received from Gemini API: %s", string(respBody))
}

func callOpenAICompatible(ctx context.Context, apiKey, model, endpoint, userPrompt, systemPrompt string) (string, error) {
	reqPayload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}

	bz, _ := json.Marshal(reqPayload)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBody, &res); err != nil {
		return "", err
	}

	if len(res.Choices) > 0 {
		return res.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("empty response received from API: %s", string(respBody))
}

func callClaude(ctx context.Context, apiKey, model, userPrompt, systemPrompt string) (string, error) {
	url := "https://api.anthropic.com/v1/messages"

	reqPayload := map[string]interface{}{
		"model":      model,
		"max_tokens": 4096,
		"system":     systemPrompt,
		"messages": []map[string]interface{}{
			{"role": "user", "content": userPrompt},
		},
	}

	bz, _ := json.Marshal(reqPayload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bz))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(respBody, &res); err != nil {
		return "", err
	}

	if len(res.Content) > 0 {
		return res.Content[0].Text, nil
	}

	return "", fmt.Errorf("empty response received from Claude API: %s", string(respBody))
}

