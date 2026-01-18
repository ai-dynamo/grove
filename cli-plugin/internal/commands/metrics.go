// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

// Package commands provides the CLI subcommands for kubectl-grove.
package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"
)

// EngineType represents the type of inference engine
type EngineType string

const (
	// EngineSGLang is the SGLang inference engine
	EngineSGLang EngineType = "sglang"
	// EngineVLLM is the vLLM inference engine
	EngineVLLM EngineType = "vllm"
	// EngineTRTLLM is the TensorRT-LLM inference engine
	EngineTRTLLM EngineType = "trtllm"
	// EngineUnknown is an unknown inference engine
	EngineUnknown EngineType = "unknown"
)

// Default metrics port for inference engines
const (
	DefaultMetricsPort = 8000
	MetricsPath        = "/metrics"
	WatchInterval      = 5 * time.Second
	ScrapeTimeout      = 10 * time.Second
)

// MetricsCmd represents the metrics command
type MetricsCmd struct {
	Clientset  kubernetes.Interface
	RestConfig *rest.Config
	Namespace  string
	Name       string
	Watch      bool
	JSON       bool
	Role       string // optional filter by role
}

// PodMetrics holds metrics scraped from a single pod
type PodMetrics struct {
	PodName            string     `json:"podName"`
	PodIP              string     `json:"podIP"`
	Role               string     `json:"role"`
	Engine             EngineType `json:"engine"`
	Throughput         float64    `json:"throughput"`         // tokens/second
	TTFTp50            float64    `json:"ttftP50"`            // time-to-first-token p50 in ms
	TTFTp99            float64    `json:"ttftP99"`            // time-to-first-token p99 in ms
	TPOTp50            float64    `json:"tpotP50"`            // time-per-output-token p50 in ms
	TPOTp99            float64    `json:"tpotP99"`            // time-per-output-token p99 in ms
	PendingRequests    int64      `json:"pendingRequests"`    // queue depth
	RunningRequests    int64      `json:"runningRequests"`    // currently processing
	GPUUtilization     float64    `json:"gpuUtilization"`     // 0-100%
	GPUMemoryUsed      float64    `json:"gpuMemoryUsed"`      // bytes
	GPUMemoryTotal     float64    `json:"gpuMemoryTotal"`     // bytes
	NumGPUs            int        `json:"numGPUs"`            // number of GPUs
	TotalTokensGenerated int64    `json:"totalTokensGenerated"`
	TotalPromptTokens    int64    `json:"totalPromptTokens"`
	ScrapeError        string     `json:"scrapeError,omitempty"`
	ScrapeTime         time.Time  `json:"scrapeTime"`
}

// AggregatedMetrics holds aggregated metrics across all pods
type AggregatedMetrics struct {
	Name            string                  `json:"name"`
	Namespace       string                  `json:"namespace"`
	Engine          EngineType              `json:"engine"`
	Timestamp       time.Time               `json:"timestamp"`
	PodCount        int                     `json:"podCount"`
	TotalGPUs       int                     `json:"totalGPUs"`
	Throughput      ThroughputMetrics       `json:"throughput"`
	Latency         LatencyMetrics          `json:"latency"`
	Queue           QueueMetrics            `json:"queue"`
	GPU             GPUMetrics              `json:"gpu,omitempty"`
	ByRole          map[string]*RoleMetrics `json:"byRole,omitempty"`
	PodMetrics      []PodMetrics            `json:"pods,omitempty"`
	FailedScrapes   int                     `json:"failedScrapes,omitempty"`
}

// ThroughputMetrics holds throughput-related metrics
type ThroughputMetrics struct {
	Aggregate float64 `json:"aggregate"` // total tokens/s across all pods
	PerGPU    float64 `json:"perGPU"`    // tokens/s per GPU
}

// LatencyMetrics holds latency-related metrics
type LatencyMetrics struct {
	TTFT PercentileMetrics `json:"ttft"` // time-to-first-token
	TPOT PercentileMetrics `json:"tpot"` // time-per-output-token
}

// PercentileMetrics holds p50 and p99 values
type PercentileMetrics struct {
	P50 float64 `json:"p50"`
	P99 float64 `json:"p99"`
}

// QueueMetrics holds queue-related metrics
type QueueMetrics struct {
	Pending int64 `json:"pending"`
	Running int64 `json:"running"`
}

// GPUMetrics holds GPU-related metrics
type GPUMetrics struct {
	Utilization float64 `json:"utilization"` // average across all GPUs
	MemoryUsed  float64 `json:"memoryUsed"`  // total bytes
	MemoryTotal float64 `json:"memoryTotal"` // total bytes
}

// RoleMetrics holds metrics aggregated by role
type RoleMetrics struct {
	Role       string            `json:"role"`
	PodCount   int               `json:"podCount"`
	GPUCount   int               `json:"gpuCount"`
	Throughput float64           `json:"throughput"`
	Latency    LatencyMetrics    `json:"latency"`
	Queue      QueueMetrics      `json:"queue"`
}

// MetricsScraper handles scraping metrics from inference pods
type MetricsScraper struct {
	clientset  kubernetes.Interface
	restConfig *rest.Config
	namespace  string
	httpClient *http.Client
}

// NewMetricsScraper creates a new MetricsScraper
func NewMetricsScraper(clientset kubernetes.Interface, restConfig *rest.Config, namespace string) *MetricsScraper {
	return &MetricsScraper{
		clientset:  clientset,
		restConfig: restConfig,
		namespace:  namespace,
		httpClient: &http.Client{
			Timeout: ScrapeTimeout,
		},
	}
}

// Run executes the metrics command
func (m *MetricsCmd) Run(ctx context.Context) error {
	if m.Name == "" {
		return fmt.Errorf("podcliqueset name is required")
	}

	scraper := NewMetricsScraper(m.Clientset, m.RestConfig, m.Namespace)

	if m.Watch {
		return m.runWatch(ctx, scraper)
	}

	return m.runOnce(ctx, scraper)
}

// runOnce executes a single metrics scrape and display
func (m *MetricsCmd) runOnce(ctx context.Context, scraper *MetricsScraper) error {
	metrics, err := scraper.ScrapeAll(ctx, m.Name, m.Role)
	if err != nil {
		return fmt.Errorf("failed to scrape metrics: %w", err)
	}

	if m.JSON {
		return m.outputJSON(metrics)
	}

	m.outputText(metrics, nil)
	return nil
}

// runWatch executes continuous metrics scraping with refresh
func (m *MetricsCmd) runWatch(ctx context.Context, scraper *MetricsScraper) error {
	var prevMetrics *AggregatedMetrics

	ticker := time.NewTicker(WatchInterval)
	defer ticker.Stop()

	// Initial scrape
	metrics, err := scraper.ScrapeAll(ctx, m.Name, m.Role)
	if err != nil {
		return fmt.Errorf("failed to scrape metrics: %w", err)
	}

	// Clear screen and display
	fmt.Print("\033[2J\033[H")
	if m.JSON {
		if err := m.outputJSON(metrics); err != nil {
			return err
		}
	} else {
		m.outputText(metrics, prevMetrics)
	}
	prevMetrics = metrics

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			metrics, err := scraper.ScrapeAll(ctx, m.Name, m.Role)
			if err != nil {
				fmt.Printf("Error scraping metrics: %v\n", err)
				continue
			}

			// Clear screen and redisplay
			fmt.Print("\033[2J\033[H")
			if m.JSON {
				if err := m.outputJSON(metrics); err != nil {
					return err
				}
			} else {
				m.outputText(metrics, prevMetrics)
			}
			prevMetrics = metrics
		}
	}
}

// outputJSON outputs metrics in JSON format
func (m *MetricsCmd) outputJSON(metrics *AggregatedMetrics) error {
	// Remove per-pod details for cleaner JSON output
	output := *metrics
	output.PodMetrics = nil

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// outputText outputs metrics in human-readable text format
func (m *MetricsCmd) outputText(metrics *AggregatedMetrics, prev *AggregatedMetrics) {
	// Header
	fmt.Printf("Live Metrics: %s (namespace: %s)\n", metrics.Name, metrics.Namespace)
	fmt.Printf("Engine: %s | Pods: %d | Last scrape: %s\n",
		metrics.Engine, metrics.PodCount, metrics.Timestamp.Format("15:04:05"))
	if metrics.FailedScrapes > 0 {
		fmt.Printf("Warning: %d pod(s) failed to scrape\n", metrics.FailedScrapes)
	}
	fmt.Println()

	// Throughput
	fmt.Println("Throughput:")
	throughputTrend := getTrendIndicator(metrics.Throughput.Aggregate, getPrevValue(prev, "throughput"))
	fmt.Printf("  Aggregate:    %s tok/s %s\n", formatNumber(metrics.Throughput.Aggregate), throughputTrend)
	if metrics.TotalGPUs > 0 {
		fmt.Printf("  Per-GPU:      %s tok/s/gpu\n", formatNumber(metrics.Throughput.PerGPU))
	}
	fmt.Println()

	// Latency
	fmt.Println("Latency (p99):")
	ttftTrend := getTrendIndicator(metrics.Latency.TTFT.P99, getPrevValue(prev, "ttft"))
	tpotTrend := getTrendIndicator(metrics.Latency.TPOT.P99, getPrevValue(prev, "tpot"))
	fmt.Printf("  TTFT:         %sms (p50: %sms) %s\n",
		formatLatency(metrics.Latency.TTFT.P99), formatLatency(metrics.Latency.TTFT.P50), ttftTrend)
	fmt.Printf("  TPOT:         %sms (p50: %sms) %s\n",
		formatLatency(metrics.Latency.TPOT.P99), formatLatency(metrics.Latency.TPOT.P50), tpotTrend)
	fmt.Println()

	// Queue
	fmt.Println("Queue:")
	fmt.Printf("  Pending:      %d requests\n", metrics.Queue.Pending)
	fmt.Printf("  Running:      %d requests\n", metrics.Queue.Running)
	fmt.Println()

	// GPU metrics if available
	if metrics.GPU.MemoryTotal > 0 {
		fmt.Println("GPU:")
		fmt.Printf("  Utilization:  %.1f%%\n", metrics.GPU.Utilization)
		fmt.Printf("  Memory:       %s / %s\n",
			formatBytes(metrics.GPU.MemoryUsed), formatBytes(metrics.GPU.MemoryTotal))
		fmt.Println()
	}

	// Per-Role Breakdown
	if len(metrics.ByRole) > 0 {
		fmt.Println("Per-Role Breakdown:")
		// Sort roles for consistent output
		roles := make([]string, 0, len(metrics.ByRole))
		for role := range metrics.ByRole {
			roles = append(roles, role)
		}
		sort.Strings(roles)

		for _, role := range roles {
			rm := metrics.ByRole[role]
			fmt.Printf("  %s (%d pods): %s tok/s, TTFT p99: %sms, TPOT p99: %sms\n",
				role, rm.PodCount,
				formatNumber(rm.Throughput),
				formatLatency(rm.Latency.TTFT.P99),
				formatLatency(rm.Latency.TPOT.P99))
		}
	}
}

// ScrapeAll scrapes metrics from all pods belonging to the PodCliqueSet
func (s *MetricsScraper) ScrapeAll(ctx context.Context, pcsName, roleFilter string) (*AggregatedMetrics, error) {
	// List pods with the PodCliqueSet label
	labelSelector := fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName)
	pods, err := s.clientset.CoreV1().Pods(s.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found for PodCliqueSet %s", pcsName)
	}

	// Filter to running pods only
	var runningPods []corev1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			// Apply role filter if specified
			if roleFilter != "" {
				podRole := getPodRole(&pod)
				if podRole != roleFilter {
					continue
				}
			}
			runningPods = append(runningPods, pod)
		}
	}

	if len(runningPods) == 0 {
		return nil, fmt.Errorf("no running pods found for PodCliqueSet %s", pcsName)
	}

	// Scrape metrics from each pod
	var podMetrics []PodMetrics
	failedScrapes := 0
	for _, pod := range runningPods {
		pm := s.ScrapePod(ctx, &pod)
		podMetrics = append(podMetrics, pm)
		if pm.ScrapeError != "" {
			failedScrapes++
		}
	}

	// Aggregate metrics
	aggregated := s.aggregateMetrics(pcsName, podMetrics)
	aggregated.FailedScrapes = failedScrapes

	return aggregated, nil
}

// ScrapePod scrapes metrics from a single pod
func (s *MetricsScraper) ScrapePod(ctx context.Context, pod *corev1.Pod) PodMetrics {
	pm := PodMetrics{
		PodName:    pod.Name,
		PodIP:      pod.Status.PodIP,
		Role:       getPodRole(pod),
		Engine:     DetectEngine(pod),
		ScrapeTime: time.Now(),
	}

	// Determine metrics port
	metricsPort := getMetricsPort(pod)

	// Try to scrape metrics directly via pod IP
	metricsURL := fmt.Sprintf("http://%s:%d%s", pod.Status.PodIP, metricsPort, MetricsPath)

	// Use port-forward if direct access fails or if we have a rest config
	body, err := s.fetchMetrics(ctx, pod, metricsPort)
	if err != nil {
		pm.ScrapeError = fmt.Sprintf("failed to fetch metrics: %v", err)
		return pm
	}

	// Parse metrics based on engine type
	rawMetrics, err := ParsePrometheusMetrics(body)
	if err != nil {
		pm.ScrapeError = fmt.Sprintf("failed to parse metrics from %s: %v", metricsURL, err)
		return pm
	}

	// Extract relevant metrics based on engine
	s.extractMetrics(&pm, rawMetrics)

	return pm
}

// fetchMetrics fetches metrics from a pod using port-forward
func (s *MetricsScraper) fetchMetrics(ctx context.Context, pod *corev1.Pod, port int) ([]byte, error) {
	// If we have a rest config, use port-forward
	if s.restConfig != nil {
		return s.fetchViaPortForward(ctx, pod, port)
	}

	// Try direct HTTP request to pod IP
	url := fmt.Sprintf("http://%s:%d%s", pod.Status.PodIP, port, MetricsPath)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// fetchViaPortForward fetches metrics using kubectl port-forward
// Note: This simplified implementation falls back to direct HTTP request.
// A full implementation would establish a port-forward tunnel using the SPDY protocol.
func (s *MetricsScraper) fetchViaPortForward(ctx context.Context, pod *corev1.Pod, port int) ([]byte, error) {
	// Attempt to verify we have the required config for port-forwarding
	if s.restConfig == nil {
		return nil, fmt.Errorf("rest config not available for port-forward")
	}

	// Verify SPDY transport can be created (this validates the config)
	_, _, err := spdy.RoundTripperFor(s.restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create round tripper: %w", err)
	}

	// For this implementation, we fall back to direct HTTP request to pod IP.
	// This works in clusters where pod networking is accessible.
	// For environments requiring port-forward (e.g., local kubectl), a full
	// port-forward implementation using channels and goroutines would be needed.
	url := fmt.Sprintf("http://%s:%d%s", pod.Status.PodIP, port, MetricsPath)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// extractMetrics extracts relevant metrics based on engine type
func (s *MetricsScraper) extractMetrics(pm *PodMetrics, rawMetrics map[string]float64) {
	switch pm.Engine {
	case EngineVLLM:
		s.extractVLLMMetrics(pm, rawMetrics)
	case EngineSGLang:
		s.extractSGLangMetrics(pm, rawMetrics)
	case EngineTRTLLM:
		s.extractTRTLLMMetrics(pm, rawMetrics)
	default:
		// Try to extract common metrics
		s.extractGenericMetrics(pm, rawMetrics)
	}
}

// extractVLLMMetrics extracts metrics from vLLM
func (s *MetricsScraper) extractVLLMMetrics(pm *PodMetrics, m map[string]float64) {
	// Throughput
	pm.Throughput = getMetricValue(m,
		"vllm:avg_generation_throughput_toks_per_s",
		"vllm_avg_generation_throughput_toks_per_s",
		"avg_generation_throughput_toks_per_s")

	// Queue metrics
	pm.PendingRequests = int64(getMetricValue(m,
		"vllm:num_requests_waiting",
		"vllm_num_requests_waiting",
		"num_requests_waiting"))
	pm.RunningRequests = int64(getMetricValue(m,
		"vllm:num_requests_running",
		"vllm_num_requests_running",
		"num_requests_running"))

	// Latency metrics (vLLM uses histograms, we need bucket values)
	pm.TTFTp50 = getMetricValue(m,
		"vllm:time_to_first_token_seconds_bucket{le=\"0.5\"}",
		"vllm_time_to_first_token_seconds_p50",
		"time_to_first_token_p50") * 1000 // convert to ms
	pm.TTFTp99 = getMetricValue(m,
		"vllm:time_to_first_token_seconds_bucket{le=\"0.99\"}",
		"vllm_time_to_first_token_seconds_p99",
		"time_to_first_token_p99") * 1000
	pm.TPOTp50 = getMetricValue(m,
		"vllm:time_per_output_token_seconds_p50",
		"vllm_time_per_output_token_seconds_p50",
		"time_per_output_token_p50") * 1000
	pm.TPOTp99 = getMetricValue(m,
		"vllm:time_per_output_token_seconds_p99",
		"vllm_time_per_output_token_seconds_p99",
		"time_per_output_token_p99") * 1000

	// GPU metrics
	pm.GPUUtilization = getMetricValue(m,
		"vllm:gpu_cache_usage_perc",
		"vllm_gpu_cache_usage_perc",
		"gpu_cache_usage_perc") * 100
	pm.GPUMemoryUsed = getMetricValue(m,
		"vllm:gpu_memory_usage_bytes",
		"vllm_gpu_memory_usage_bytes")
	pm.GPUMemoryTotal = getMetricValue(m,
		"vllm:gpu_memory_total_bytes",
		"vllm_gpu_memory_total_bytes")

	// Token counts
	pm.TotalTokensGenerated = int64(getMetricValue(m,
		"vllm:generation_tokens_total",
		"vllm_generation_tokens_total"))
	pm.TotalPromptTokens = int64(getMetricValue(m,
		"vllm:prompt_tokens_total",
		"vllm_prompt_tokens_total"))
}

// extractSGLangMetrics extracts metrics from SGLang
func (s *MetricsScraper) extractSGLangMetrics(pm *PodMetrics, m map[string]float64) {
	// Throughput
	pm.Throughput = getMetricValue(m,
		"sglang_generation_throughput_tokens_per_second",
		"sglang:generation_throughput_tokens_per_second",
		"generation_throughput_tokens_per_second")

	// Queue metrics
	pm.PendingRequests = int64(getMetricValue(m,
		"sglang_num_waiting_requests",
		"sglang:num_waiting_requests",
		"num_waiting_requests"))
	pm.RunningRequests = int64(getMetricValue(m,
		"sglang_num_running_requests",
		"sglang:num_running_requests",
		"num_running_requests"))

	// Latency metrics
	pm.TTFTp50 = getMetricValue(m,
		"sglang_time_to_first_token_p50_ms",
		"sglang:time_to_first_token_p50_ms",
		"time_to_first_token_p50_ms")
	pm.TTFTp99 = getMetricValue(m,
		"sglang_time_to_first_token_p99_ms",
		"sglang:time_to_first_token_p99_ms",
		"time_to_first_token_p99_ms")
	pm.TPOTp50 = getMetricValue(m,
		"sglang_time_per_output_token_p50_ms",
		"sglang:time_per_output_token_p50_ms",
		"time_per_output_token_p50_ms")
	pm.TPOTp99 = getMetricValue(m,
		"sglang_time_per_output_token_p99_ms",
		"sglang:time_per_output_token_p99_ms",
		"time_per_output_token_p99_ms")

	// GPU metrics
	pm.GPUUtilization = getMetricValue(m,
		"sglang_gpu_utilization",
		"sglang:gpu_utilization") * 100
	pm.GPUMemoryUsed = getMetricValue(m,
		"sglang_gpu_memory_used_bytes",
		"sglang:gpu_memory_used_bytes")
	pm.GPUMemoryTotal = getMetricValue(m,
		"sglang_gpu_memory_total_bytes",
		"sglang:gpu_memory_total_bytes")
}

// extractTRTLLMMetrics extracts metrics from TensorRT-LLM
func (s *MetricsScraper) extractTRTLLMMetrics(pm *PodMetrics, m map[string]float64) {
	// Throughput
	pm.Throughput = getMetricValue(m,
		"triton_inference_count",
		"trtllm_generation_throughput",
		"nv_inference_exec_count")

	// Queue metrics
	pm.PendingRequests = int64(getMetricValue(m,
		"triton_request_queue_length",
		"trtllm_waiting_requests",
		"nv_inference_pending_request_count"))
	pm.RunningRequests = int64(getMetricValue(m,
		"triton_running_requests",
		"trtllm_running_requests",
		"nv_inference_request_success"))

	// Latency metrics
	pm.TTFTp50 = getMetricValue(m,
		"trtllm_time_to_first_token_p50_ms",
		"triton_first_token_latency_p50")
	pm.TTFTp99 = getMetricValue(m,
		"trtllm_time_to_first_token_p99_ms",
		"triton_first_token_latency_p99")
	pm.TPOTp50 = getMetricValue(m,
		"trtllm_time_per_output_token_p50_ms",
		"triton_generation_latency_p50")
	pm.TPOTp99 = getMetricValue(m,
		"trtllm_time_per_output_token_p99_ms",
		"triton_generation_latency_p99")

	// GPU metrics
	pm.GPUUtilization = getMetricValue(m,
		"nv_gpu_utilization",
		"triton_gpu_utilization") * 100
	pm.GPUMemoryUsed = getMetricValue(m,
		"nv_gpu_memory_used_bytes",
		"triton_gpu_memory_used_bytes")
	pm.GPUMemoryTotal = getMetricValue(m,
		"nv_gpu_memory_total_bytes",
		"triton_gpu_memory_total_bytes")
}

// extractGenericMetrics extracts metrics using common metric names
func (s *MetricsScraper) extractGenericMetrics(pm *PodMetrics, m map[string]float64) {
	// Try common throughput metrics
	pm.Throughput = getMetricValue(m,
		"generation_throughput_tokens_per_second",
		"throughput_tokens_per_second",
		"avg_generation_throughput")

	// Queue metrics
	pm.PendingRequests = int64(getMetricValue(m,
		"num_waiting_requests",
		"pending_requests",
		"request_queue_length"))
	pm.RunningRequests = int64(getMetricValue(m,
		"num_running_requests",
		"running_requests",
		"active_requests"))

	// Latency metrics
	pm.TTFTp99 = getMetricValue(m,
		"time_to_first_token_p99_ms",
		"ttft_p99_ms",
		"first_token_latency_p99")
	pm.TPOTp99 = getMetricValue(m,
		"time_per_output_token_p99_ms",
		"tpot_p99_ms",
		"generation_latency_p99")
}

// aggregateMetrics aggregates metrics from all pods
func (s *MetricsScraper) aggregateMetrics(pcsName string, pods []PodMetrics) *AggregatedMetrics {
	agg := &AggregatedMetrics{
		Name:      pcsName,
		Namespace: s.namespace,
		Timestamp: time.Now(),
		ByRole:    make(map[string]*RoleMetrics),
	}

	if len(pods) == 0 {
		return agg
	}

	// Determine engine from first successful scrape
	for _, pm := range pods {
		if pm.ScrapeError == "" && pm.Engine != EngineUnknown {
			agg.Engine = pm.Engine
			break
		}
	}

	// Aggregate metrics
	var totalThroughput float64
	var totalTTFTp50, totalTTFTp99 float64
	var totalTPOTp50, totalTPOTp99 float64
	var totalPending, totalRunning int64
	var totalGPUUtil, totalGPUMemUsed, totalGPUMemTotal float64
	var gpuCount int
	var latencyCount int

	for _, pm := range pods {
		if pm.ScrapeError != "" {
			continue
		}

		agg.PodCount++
		agg.TotalGPUs += pm.NumGPUs
		if pm.NumGPUs == 0 {
			agg.TotalGPUs++ // Assume at least 1 GPU per pod if not reported
		}

		totalThroughput += pm.Throughput
		totalPending += pm.PendingRequests
		totalRunning += pm.RunningRequests

		// Latency (average across pods with valid latency)
		if pm.TTFTp99 > 0 || pm.TPOTp99 > 0 {
			totalTTFTp50 += pm.TTFTp50
			totalTTFTp99 += pm.TTFTp99
			totalTPOTp50 += pm.TPOTp50
			totalTPOTp99 += pm.TPOTp99
			latencyCount++
		}

		// GPU metrics
		if pm.GPUMemoryTotal > 0 {
			totalGPUUtil += pm.GPUUtilization
			totalGPUMemUsed += pm.GPUMemoryUsed
			totalGPUMemTotal += pm.GPUMemoryTotal
			gpuCount++
		}

		// Per-role aggregation
		role := pm.Role
		if role == "" {
			role = "default"
		}
		if _, ok := agg.ByRole[role]; !ok {
			agg.ByRole[role] = &RoleMetrics{Role: role}
		}
		rm := agg.ByRole[role]
		rm.PodCount++
		rm.GPUCount += pm.NumGPUs
		if pm.NumGPUs == 0 {
			rm.GPUCount++
		}
		rm.Throughput += pm.Throughput
		rm.Queue.Pending += pm.PendingRequests
		rm.Queue.Running += pm.RunningRequests
	}

	// Set aggregated values
	agg.Throughput.Aggregate = totalThroughput
	if agg.TotalGPUs > 0 {
		agg.Throughput.PerGPU = totalThroughput / float64(agg.TotalGPUs)
	}

	if latencyCount > 0 {
		agg.Latency.TTFT.P50 = totalTTFTp50 / float64(latencyCount)
		agg.Latency.TTFT.P99 = totalTTFTp99 / float64(latencyCount)
		agg.Latency.TPOT.P50 = totalTPOTp50 / float64(latencyCount)
		agg.Latency.TPOT.P99 = totalTPOTp99 / float64(latencyCount)
	}

	agg.Queue.Pending = totalPending
	agg.Queue.Running = totalRunning

	if gpuCount > 0 {
		agg.GPU.Utilization = totalGPUUtil / float64(gpuCount)
		agg.GPU.MemoryUsed = totalGPUMemUsed
		agg.GPU.MemoryTotal = totalGPUMemTotal
	}

	// Average latency per role
	for _, pm := range pods {
		if pm.ScrapeError != "" {
			continue
		}
		role := pm.Role
		if role == "" {
			role = "default"
		}
		rm := agg.ByRole[role]
		if pm.TTFTp99 > 0 {
			// Simple average for now
			rm.Latency.TTFT.P50 = (rm.Latency.TTFT.P50*(float64(rm.PodCount)-1) + pm.TTFTp50) / float64(rm.PodCount)
			rm.Latency.TTFT.P99 = (rm.Latency.TTFT.P99*(float64(rm.PodCount)-1) + pm.TTFTp99) / float64(rm.PodCount)
			rm.Latency.TPOT.P50 = (rm.Latency.TPOT.P50*(float64(rm.PodCount)-1) + pm.TPOTp50) / float64(rm.PodCount)
			rm.Latency.TPOT.P99 = (rm.Latency.TPOT.P99*(float64(rm.PodCount)-1) + pm.TPOTp99) / float64(rm.PodCount)
		}
	}

	agg.PodMetrics = pods
	return agg
}

// ParsePrometheusMetrics parses Prometheus text format metrics
func ParsePrometheusMetrics(body []byte) (map[string]float64, error) {
	metrics := make(map[string]float64)
	scanner := bufio.NewScanner(bytes.NewReader(body))

	// Regex to match metric lines
	// Format: metric_name{labels} value
	// or: metric_name value
	metricRegex := regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*(?:\{[^}]*\})?)[\s]+([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		matches := metricRegex.FindStringSubmatch(line)
		if len(matches) >= 3 {
			metricName := matches[1]
			valueStr := matches[2]

			value, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				continue
			}

			metrics[metricName] = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning metrics: %w", err)
	}

	return metrics, nil
}

// DetectEngine detects the inference engine from pod labels/annotations or container image
func DetectEngine(pod *corev1.Pod) EngineType {
	// Check labels first
	if engine, ok := pod.Labels["grove.io/inference-engine"]; ok {
		return parseEngineType(engine)
	}
	if engine, ok := pod.Labels["app.kubernetes.io/component"]; ok {
		return parseEngineType(engine)
	}

	// Check annotations
	if engine, ok := pod.Annotations["grove.io/inference-engine"]; ok {
		return parseEngineType(engine)
	}

	// Check container images
	for _, container := range pod.Spec.Containers {
		image := strings.ToLower(container.Image)
		if strings.Contains(image, "vllm") {
			return EngineVLLM
		}
		if strings.Contains(image, "sglang") {
			return EngineSGLang
		}
		if strings.Contains(image, "triton") || strings.Contains(image, "trtllm") || strings.Contains(image, "tensorrt") {
			return EngineTRTLLM
		}
	}

	// Check container commands/args
	for _, container := range pod.Spec.Containers {
		cmdArgs := append(container.Command, container.Args...)
		for _, arg := range cmdArgs {
			argLower := strings.ToLower(arg)
			if strings.Contains(argLower, "vllm") {
				return EngineVLLM
			}
			if strings.Contains(argLower, "sglang") {
				return EngineSGLang
			}
			if strings.Contains(argLower, "triton") || strings.Contains(argLower, "trtllm") {
				return EngineTRTLLM
			}
		}
	}

	return EngineUnknown
}

// parseEngineType converts a string to EngineType
func parseEngineType(s string) EngineType {
	switch strings.ToLower(s) {
	case "sglang":
		return EngineSGLang
	case "vllm":
		return EngineVLLM
	case "trtllm", "tensorrt-llm", "triton":
		return EngineTRTLLM
	default:
		return EngineUnknown
	}
}

// getPodRole extracts the role from pod labels
func getPodRole(pod *corev1.Pod) string {
	// Check common role labels
	if role, ok := pod.Labels["grove.io/role"]; ok {
		return role
	}
	if role, ok := pod.Labels["grove.io/clique-role"]; ok {
		return role
	}
	if role, ok := pod.Labels["app.kubernetes.io/component"]; ok {
		return role
	}

	// Try to extract from pod name (e.g., my-inference-0-prefill-xyz)
	parts := strings.Split(pod.Name, "-")
	for _, part := range parts {
		lower := strings.ToLower(part)
		if lower == "prefill" || lower == "decode" || lower == "router" || lower == "frontend" {
			return lower
		}
	}

	return ""
}

// getMetricsPort determines the metrics port for a pod
func getMetricsPort(pod *corev1.Pod) int {
	// Check annotation
	if portStr, ok := pod.Annotations["prometheus.io/port"]; ok {
		if port, err := strconv.Atoi(portStr); err == nil {
			return port
		}
	}

	// Check container ports
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.Name == "metrics" || port.Name == "prometheus" {
				return int(port.ContainerPort)
			}
			// Common metrics ports
			if port.ContainerPort == 8000 || port.ContainerPort == 9090 || port.ContainerPort == 8080 {
				return int(port.ContainerPort)
			}
		}
	}

	return DefaultMetricsPort
}

// getMetricValue returns the first non-zero value from the given metric names
func getMetricValue(m map[string]float64, names ...string) float64 {
	for _, name := range names {
		if val, ok := m[name]; ok && val != 0 {
			return val
		}
	}
	return 0
}

// getTrendIndicator returns a trend arrow based on current vs previous value
func getTrendIndicator(current, prev float64) string {
	if prev == 0 {
		return ""
	}
	diff := current - prev
	threshold := prev * 0.05 // 5% threshold

	if diff > threshold {
		return "[+]"
	} else if diff < -threshold {
		return "[-]"
	}
	return "[=]"
}

// getPrevValue extracts a specific value from previous metrics
func getPrevValue(prev *AggregatedMetrics, metric string) float64 {
	if prev == nil {
		return 0
	}
	switch metric {
	case "throughput":
		return prev.Throughput.Aggregate
	case "ttft":
		return prev.Latency.TTFT.P99
	case "tpot":
		return prev.Latency.TPOT.P99
	}
	return 0
}

// formatNumber formats a number with thousands separators
func formatNumber(n float64) string {
	if n < 1000 {
		return fmt.Sprintf("%.1f", n)
	}
	return fmt.Sprintf("%.0f", n)
}

// formatLatency formats latency in milliseconds
func formatLatency(ms float64) string {
	if ms < 1 {
		return fmt.Sprintf("%.2f", ms)
	}
	if ms < 10 {
		return fmt.Sprintf("%.1f", ms)
	}
	return fmt.Sprintf("%.0f", ms)
}

// formatBytes formats bytes in human-readable form
func formatBytes(bytes float64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	if bytes < KB {
		return fmt.Sprintf("%.0f B", bytes)
	}
	if bytes < MB {
		return fmt.Sprintf("%.1f KB", bytes/KB)
	}
	if bytes < GB {
		return fmt.Sprintf("%.1f MB", bytes/MB)
	}
	return fmt.Sprintf("%.2f GB", bytes/GB)
}
