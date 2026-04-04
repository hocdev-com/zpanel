package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDownloadFileUsesParallelRanges(t *testing.T) {
	data := bytes.Repeat([]byte("parallel-download-"), 700000)
	server, state := newDownloadTestServer(data, true, nil)
	defer server.Close()

	outPath := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadFile(server.URL+"/file.zip", outPath, "Test Archive", 10, 80, nil); err != nil {
		t.Fatalf("download file: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file does not match source data")
	}
	if state.rangeRequests() < 3 {
		t.Fatalf("expected parallel range requests, got %d", state.rangeRequests())
	}
}

func TestDownloadFileFallsBackWhenRangeUnsupported(t *testing.T) {
	data := bytes.Repeat([]byte("single-stream-download-"), 40000)
	server, _ := newDownloadTestServer(data, false, nil)
	defer server.Close()

	tempDir := t.TempDir()
	outPath := filepath.Join(tempDir, "archive.zip")
	if err := os.WriteFile(outPath+".part", []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale part: %v", err)
	}

	if err := downloadFile(server.URL+"/file.zip", outPath, "Test Archive", 10, 80, nil); err != nil {
		t.Fatalf("download file: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file does not match source data")
	}
}

func TestDownloadFileResumesSequentialRangeDownload(t *testing.T) {
	data := bytes.Repeat([]byte("resume-download-"), 120000)
	server, state := newDownloadTestServer(data, true, nil)
	defer server.Close()

	tempDir := t.TempDir()
	outPath := filepath.Join(tempDir, "archive.zip")
	resumeBytes := len(data) / 3
	if err := os.WriteFile(outPath+".part", data[:resumeBytes], 0o644); err != nil {
		t.Fatalf("write part file: %v", err)
	}

	if err := downloadFile(server.URL+"/file.zip", outPath, "Test Archive", 10, 80, nil); err != nil {
		t.Fatalf("download file: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file does not match source data")
	}

	if !state.hasRangeWithPrefix(fmt.Sprintf("bytes=%d-", resumeBytes)) {
		t.Fatalf("expected resumed request starting at byte %d", resumeBytes)
	}
}

func TestDownloadFileRetriesTransientRangeFailures(t *testing.T) {
	data := bytes.Repeat([]byte("retry-download-"), 700000)
	failOnce := map[string]int{
		"bytes=0-2097151":       1,
		"bytes=2097152-4194303": 1,
		"bytes=4194304-6291455": 1,
	}
	server, _ := newDownloadTestServer(data, true, failOnce)
	defer server.Close()

	outPath := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadFile(server.URL+"/file.zip", outPath, "Test Archive", 10, 80, nil); err != nil {
		t.Fatalf("download file: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file does not match source data")
	}
}

type downloadTestServerState struct {
	mu          sync.Mutex
	rangeHits   []string
	failByRange map[string]int
}

func (s *downloadTestServerState) recordRange(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rangeHits = append(s.rangeHits, value)
}

func (s *downloadTestServerState) shouldFail(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failByRange == nil {
		return false
	}
	remaining := s.failByRange[value]
	if remaining <= 0 {
		return false
	}
	s.failByRange[value] = remaining - 1
	return true
}

func (s *downloadTestServerState) rangeRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, item := range s.rangeHits {
		if item != "bytes=0-0" {
			count++
		}
	}
	return count
}

func (s *downloadTestServerState) hasRangeWithPrefix(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.rangeHits {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func newDownloadTestServer(data []byte, supportsRanges bool, failByRange map[string]int) (*httptest.Server, *downloadTestServerState) {
	state := &downloadTestServerState{
		failByRange: map[string]int{},
	}
	for key, value := range failByRange {
		state.failByRange[key] = value
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
		if rangeHeader != "" {
			state.recordRange(rangeHeader)
		}

		if state.shouldFail(rangeHeader) {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}

		if !supportsRanges || rangeHeader == "" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
			return
		}

		start, end, ok := parseTestRange(rangeHeader, int64(len(data)))
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, bytes.NewReader(data[start:end+1]))
		}
	})

	return httptest.NewServer(handler), state
}

func parseTestRange(value string, total int64) (int64, int64, bool) {
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(value, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}

	var start int64
	if _, err := fmt.Sscanf(parts[0], "%d", &start); err != nil {
		return 0, 0, false
	}

	end := total - 1
	if strings.TrimSpace(parts[1]) != "" {
		if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
			return 0, 0, false
		}
	}

	if start < 0 || start >= total || end < start {
		return 0, 0, false
	}
	if end >= total {
		end = total - 1
	}
	return start, end, true
}
