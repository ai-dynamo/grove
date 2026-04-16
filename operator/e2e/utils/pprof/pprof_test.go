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

package pprof_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/ai-dynamo/grove/operator/e2e/utils/pprof"
)

// fakeBinaryProfile builds a minimal valid google.v1.Profile in binary protobuf.
// This mirrors what Pyroscope returns with Content-Type: application/proto.
func fakeBinaryProfile() []byte {
	var b []byte
	// field 6: string_table (repeated string) — at least one empty string required
	b = protowire.AppendTag(b, 6, protowire.BytesType)
	b = protowire.AppendString(b, "")
	return b
}

func TestDownloader_DownloadForPhase(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	profile := fakeBinaryProfile()

	tests := []struct {
		name       string
		httpStatus int
		wantFiles  []string
	}{
		{
			name:       "downloads all profile types for phase",
			httpStatus: http.StatusOK,
			wantFiles: []string{
				"pprof-run-001-deploy-cpu.pprof.gz",
				"pprof-run-001-deploy-memory.pprof.gz",
				"pprof-run-001-deploy-goroutine.pprof.gz",
			},
		},
		{
			name:       "HTTP 404 — files not written",
			httpStatus: http.StatusNotFound,
			wantFiles:  nil,
		},
		{
			name:       "HTTP 500 — files not written",
			httpStatus: http.StatusInternalServerError,
			wantFiles:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.httpStatus)
				if tt.httpStatus == http.StatusOK {
					_, _ = w.Write(profile)
				}
			}))
			defer srv.Close()

			dir := t.TempDir()
			d := pprof.NewDownloader(srv.URL, "run-001", pprof.WithOutputDir(dir))
			d.DownloadForPhase(context.Background(), "deploy", now.Add(-5*time.Minute), now)

			entries, err := os.ReadDir(dir)
			require.NoError(t, err)

			if tt.wantFiles == nil {
				assert.Empty(t, entries)
				return
			}
			for _, f := range tt.wantFiles {
				assert.FileExists(t, filepath.Join(dir, f))
			}
		})
	}
}

func TestDownloader_DownloadForPhase_ZeroEndTime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := pprof.NewDownloader("http://localhost:4040", "run-001", pprof.WithOutputDir(dir))

	// Zero end time — should be a no-op, no files created.
	d.DownloadForPhase(context.Background(), "deploy", time.Now(), time.Time{})

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDownloader_DownloadForPhase_EndBeforeStart(t *testing.T) {
	t.Parallel()

	now := time.Now()
	dir := t.TempDir()
	d := pprof.NewDownloader("http://localhost:4040", "run-001", pprof.WithOutputDir(dir))

	// end before start — should be a no-op.
	d.DownloadForPhase(context.Background(), "deploy", now, now.Add(-1*time.Second))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDownloader_DownloadForPhase_FileNameFormat(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	profile := fakeBinaryProfile()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(profile)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := pprof.NewDownloader(srv.URL, "run-20260312", pprof.WithOutputDir(dir))
	d.DownloadForPhase(context.Background(), "delete", now.Add(-2*time.Minute), now)

	assert.FileExists(t, filepath.Join(dir, "pprof-run-20260312-delete-cpu.pprof.gz"))
	assert.FileExists(t, filepath.Join(dir, "pprof-run-20260312-delete-memory.pprof.gz"))
	assert.FileExists(t, filepath.Join(dir, "pprof-run-20260312-delete-goroutine.pprof.gz"))
}

func TestDownloader_DownloadForPhase_OutputIsValidGzip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	profile := fakeBinaryProfile()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(profile)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := pprof.NewDownloader(srv.URL, "gz-test", pprof.WithOutputDir(dir))
	d.DownloadForPhase(context.Background(), "phase", now.Add(-time.Minute), now)

	cpuPath := filepath.Join(dir, "pprof-gz-test-phase-cpu.pprof.gz")
	data, err := os.ReadFile(cpuPath)
	require.NoError(t, err)

	gz, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err, "output should be valid gzip")

	uncompressed, err := io.ReadAll(gz)
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	assert.Equal(t, profile, uncompressed, "uncompressed content should match the binary profile")
}

func TestDownloader_UsesConnectProtoEncoding(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	profile := fakeBinaryProfile()

	type captured struct {
		Path           string
		Method         string
		ContentType    string
		ConnectVersion string
		Body           []byte
	}

	var mu sync.Mutex
	var requests []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, captured{
			Path:           r.URL.Path,
			Method:         r.Method,
			ContentType:    r.Header.Get("Content-Type"),
			ConnectVersion: r.Header.Get("Connect-Protocol-Version"),
			Body:           body,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(profile)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := pprof.NewDownloader(srv.URL, "r1",
		pprof.WithOutputDir(dir),
		pprof.WithAppName("my-namespace"),
	)
	d.DownloadForPhase(context.Background(), "phase1", now.Add(-time.Minute), now)

	require.Len(t, requests, 3)
	for _, req := range requests {
		assert.Equal(t, "POST", req.Method)
		assert.Equal(t, "/querier.v1.QuerierService/SelectMergeProfile", req.Path)
		assert.Equal(t, "application/proto", req.ContentType)
		assert.Equal(t, "1", req.ConnectVersion)

		// Verify request body is valid protobuf with expected fields
		remaining := req.Body
		foundLabelSelector := false
		for len(remaining) > 0 {
			num, typ, n := protowire.ConsumeTag(remaining)
			require.Greater(t, n, 0, "should consume tag")
			remaining = remaining[n:]

			switch typ {
			case protowire.BytesType:
				val, n := protowire.ConsumeBytes(remaining)
				require.Greater(t, n, 0)
				remaining = remaining[n:]
				if num == 2 {
					assert.Contains(t, string(val), "my-namespace")
					foundLabelSelector = true
				}
			case protowire.VarintType:
				_, n := protowire.ConsumeVarint(remaining)
				require.Greater(t, n, 0)
				remaining = remaining[n:]
			default:
				t.Fatalf("unexpected wire type %d for field %d", typ, num)
			}
		}
		assert.True(t, foundLabelSelector, "request should contain label_selector with app name")
	}
}

func TestProfileType_QueryPrefix(t *testing.T) {
	t.Parallel()

	assert.Contains(t, pprof.ProfileCPU.QueryPrefix(), "cpu")
	assert.Contains(t, pprof.ProfileMemory.QueryPrefix(), "memory")
	assert.Contains(t, pprof.ProfileGoroutine.QueryPrefix(), "goroutine")
	assert.Empty(t, pprof.ProfileType("unknown").QueryPrefix())
}
