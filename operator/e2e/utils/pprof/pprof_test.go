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
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ai-dynamo/grove/operator/e2e/utils/pprof"
)

func TestDownloader_DownloadForPhase(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		httpStatus int
		wantFiles  []string
	}{
		{
			name:       "downloads cpu and memory for phase",
			httpStatus: http.StatusOK,
			wantFiles: []string{
				"pprof-run-001-deploy-cpu.pprof",
				"pprof-run-001-deploy-memory.pprof",
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

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.httpStatus)
				if tt.httpStatus == http.StatusOK {
					_, _ = w.Write([]byte("fake-pprof-data"))
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-pprof"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := pprof.NewDownloader(srv.URL, "run-20260312", pprof.WithOutputDir(dir))
	d.DownloadForPhase(context.Background(), "delete", now.Add(-2*time.Minute), now)

	assert.FileExists(t, filepath.Join(dir, "pprof-run-20260312-delete-cpu.pprof"))
	assert.FileExists(t, filepath.Join(dir, "pprof-run-20260312-delete-memory.pprof"))
}

func TestBuildQuery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		profileType pprof.ProfileType
		wantPrefix  string
	}{
		{
			name:        "CPU query contains cpu prefix",
			profileType: pprof.ProfileCPU,
			wantPrefix:  "process_cpu",
		},
		{
			name:        "Memory query contains memory prefix",
			profileType: pprof.ProfileMemory,
			wantPrefix:  "memory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotURL = r.URL.String()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			dir := t.TempDir()
			d := pprof.NewDownloader(srv.URL, "r1",
				pprof.WithOutputDir(dir),
				pprof.WithAppName("my-app"),
			)
			d.DownloadForPhase(context.Background(), "phase1", now.Add(-time.Minute), now)

			// Verify URL reached the server with correct query parameter.
			require.NotEmpty(t, gotURL)
			assert.True(t, strings.Contains(gotURL, "/pyroscope/render"),
				"URL should contain /pyroscope/render, got: %s", gotURL)
			assert.Contains(t, gotURL, "format=pprof")
			assert.Contains(t, gotURL, "my-app")
			assert.Contains(t, gotURL, tt.wantPrefix)
		})
	}
}

func TestProfileType_QueryPrefix(t *testing.T) {
	t.Parallel()

	assert.Contains(t, pprof.ProfileCPU.QueryPrefix(), "cpu")
	assert.Contains(t, pprof.ProfileMemory.QueryPrefix(), "memory")
	assert.Empty(t, pprof.ProfileType("unknown").QueryPrefix())
}
