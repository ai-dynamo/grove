// /*
// Copyright 2026 The Grove Authors.
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

// Package pprof downloads CPU and memory profiles from a Pyroscope endpoint
// after each test phase completes.
package pprof

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
)

const (
	defaultAppName         = "grove-system/grove-operator"
	defaultDownloadTimeout = 30 * time.Second
	memoryLookbackWindow   = 60 * time.Second
)

// ProfileType identifies a pprof profile kind exposed by Pyroscope.
// String value appears in output file names.
type ProfileType string

// Supported profile types for Pyroscope downloads.
const (
	ProfileCPU       ProfileType = "cpu"
	ProfileMemory    ProfileType = "memory"
	ProfileGoroutine ProfileType = "goroutine"
)

// QueryPrefix returns the Pyroscope metric selector prefix for this profile type.
// Single source of truth — switch-based, not a mutable package-level map.
func (p ProfileType) QueryPrefix() string {
	switch p {
	case ProfileCPU:
		return "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	case ProfileMemory:
		return "memory:inuse_space:bytes:space:bytes"
	case ProfileGoroutine:
		return "goroutine:goroutine:count:goroutine:count"
	default:
		return ""
	}
}

// HTTPClient is the interface for executing HTTP requests.
// *http.Client satisfies this interface.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Option is a functional option for NewDownloader.
type Option func(*Downloader)

// WithAppName overrides the Pyroscope label filter value (default: "grove-system").
// Used as the namespace label selector in Pyroscope queries.
func WithAppName(name string) Option {
	return func(d *Downloader) { d.appName = name }
}

// WithOutputDir sets the directory for downloaded profile files (default: os.TempDir()).
func WithOutputDir(dir string) Option {
	return func(d *Downloader) { d.outputDir = dir }
}

// WithLogger sets the logger for the downloader.
func WithLogger(l logr.Logger) Option {
	return func(d *Downloader) { d.logger = l }
}

// WithHTTPClient replaces the default HTTP client (useful for testing).
func WithHTTPClient(h HTTPClient) Option {
	return func(d *Downloader) { d.httpClient = h }
}

// WithDownloadTimeout sets the per-download timeout (default: 30s).
func WithDownloadTimeout(t time.Duration) Option {
	return func(d *Downloader) { d.timeout = t }
}

// Downloader downloads CPU and memory pprof profiles from a Pyroscope endpoint.
//
// No K8s dependencies — only knows about an HTTP baseURL and a runID.
// Port-forwarding is the caller's responsibility.
// Zero value is invalid; always construct via NewDownloader.
type Downloader struct {
	baseURL    string
	runID      string
	appName    string
	outputDir  string
	timeout    time.Duration
	logger     logr.Logger
	httpClient HTTPClient
}

// NewDownloader returns a Downloader targeting baseURL with the given runID.
// runID is embedded in every output filename for traceability.
func NewDownloader(baseURL, runID string, opts ...Option) *Downloader {
	d := &Downloader{
		baseURL:    baseURL,
		runID:      runID,
		appName:    defaultAppName,
		outputDir:  os.TempDir(),
		timeout:    defaultDownloadTimeout,
		logger:     logr.Discard(),
		httpClient: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// DownloadForPhase downloads pprof profiles for a completed phase in parallel:
//   - CPU profile spanning [start, end] (full phase window)
//   - Memory snapshot at [end-60s, end] (lookback window)
//
// Non-fatal: all errors are logged, never returned.
// Output files: pprof-<runID>-<phaseName>-cpu.pprof
//
//	pprof-<runID>-<phaseName>-memory.pprof
func (d *Downloader) DownloadForPhase(ctx context.Context, phaseName string, start, end time.Time) {
	if end.IsZero() || !end.After(start) {
		d.logger.Error(
			fmt.Errorf("invalid phase window: start=%v end=%v", start, end),
			"skipping profile download",
			"phase", phaseName,
		)
		return
	}

	type job struct {
		profileType ProfileType
		from, to    time.Time
	}

	jobs := []job{
		{ProfileCPU, start, end},
		{ProfileMemory, end.Add(-memoryLookbackWindow), end},
		{ProfileGoroutine, end.Add(-memoryLookbackWindow), end},
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, j := range jobs {
		g.Go(func() error {
			d.downloadProfile(gctx, phaseName, j.profileType, j.from, j.to)
			return nil
		})
	}
	//nolint:errcheck // g.Wait() always returns nil — downloadProfile never returns errors.
	_ = g.Wait()
}

// downloadProfile fetches one profile via the Pyroscope SelectMergeProfile
// Connect RPC endpoint using binary protobuf encoding and writes the gzipped
// pprof protobuf to outputDir.
// The response is a raw google.v1.Profile binary — standard pprof format.
// All errors are logged; nothing is returned so DownloadForPhase remains non-fatal.
func (d *Downloader) downloadProfile(ctx context.Context, phaseName string, p ProfileType, from, to time.Time) {
	body := marshalSelectMergeRequest(d.appName, p, from, to)

	dlCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodPost, buildURL(d.baseURL), bytes.NewReader(body))
	if err != nil {
		d.logger.Error(err, "create profile request", "phase", phaseName, "type", p)
		return
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.logger.Error(err, "download profile", "phase", phaseName, "type", p)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		d.logger.Error(
			fmt.Errorf("status %d", resp.StatusCode),
			"unexpected response downloading profile",
			"phase", phaseName,
			"type", p,
		)
		return
	}

	// Response body is raw binary protobuf google.v1.Profile — standard pprof format.
	pprofData, err := io.ReadAll(resp.Body)
	if err != nil {
		d.logger.Error(err, "read profile response body", "phase", phaseName, "type", p)
		return
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(pprofData); err != nil {
		d.logger.Error(err, "gzip profile", "phase", phaseName, "type", p)
		return
	}
	if err := gz.Close(); err != nil {
		d.logger.Error(err, "gzip close", "phase", phaseName, "type", p)
		return
	}

	filename := fmt.Sprintf("pprof-%s-%s-%s.pprof.gz", d.runID, phaseName, string(p))
	path := filepath.Join(d.outputDir, filename)

	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		d.logger.Error(err, "write profile file", "phase", phaseName, "type", p, "path", path)
		return
	}

	d.logger.Info("profile saved", "phase", phaseName, "type", p, "path", path, "size", len(pprofData))
}
