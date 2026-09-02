package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpdateVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-update currently supports macOS and Linux")
	}
	candidate := []byte("#!/bin/sh\necho 'termlinks 0.6.0'\n")
	archive := makeArchive(t, "termlinks", candidate)
	server, apiURL, requests := releaseServer(t, archive, checksumLine(archive, "termlinks_0.6.0_test_test.tar.gz"))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "termlinks")
	writeExecutable(t, executable, []byte("old executable"))
	result, err := Update(context.Background(), Options{
		CurrentVersion: "0.5.0",
		ExecutablePath: executable,
		GOOS:           "test",
		GOARCH:         "test",
		Client:         server.Client(),
		APIURL:         apiURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.From != "0.5.0" || result.To != "0.6.0" {
		t.Fatalf("unexpected result: %+v", result)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, candidate) {
		t.Fatalf("installed executable differs: %q", contents)
	}
	if got := atomic.LoadInt32(requests); got != 3 {
		t.Fatalf("got %d requests, want 3", got)
	}
}

func TestUpdateChecksumFailurePreservesExecutable(t *testing.T) {
	archive := makeArchive(t, "termlinks", []byte("#!/bin/sh\necho 'termlinks 0.6.0'\n"))
	wrongChecksum := strings.Repeat("0", 64) + "  termlinks_0.6.0_test_test.tar.gz\n"
	server, apiURL, _ := releaseServer(t, archive, wrongChecksum)
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "termlinks")
	writeExecutable(t, executable, []byte("old executable"))

	_, err := Update(context.Background(), Options{CurrentVersion: "0.5.0", ExecutablePath: executable, GOOS: "test", GOARCH: "test", Client: server.Client(), APIURL: apiURL})
	if err == nil || !strings.Contains(err.Error(), "SHA-256 verification failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	contents, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "old executable" {
		t.Fatalf("current executable changed after failed verification: %q", contents)
	}
}

func TestUpdateRejectsUnsafeArchive(t *testing.T) {
	archive := makeArchive(t, "../termlinks", []byte("bad"))
	server, apiURL, _ := releaseServer(t, archive, checksumLine(archive, "termlinks_0.6.0_test_test.tar.gz"))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "termlinks")
	writeExecutable(t, executable, []byte("old executable"))

	_, err := Update(context.Background(), Options{CurrentVersion: "0.5.0", ExecutablePath: executable, GOOS: "test", GOARCH: "test", Client: server.Client(), APIURL: apiURL})
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateDoesNotDownloadWhenCurrent(t *testing.T) {
	archive := makeArchive(t, "termlinks", []byte("unused"))
	server, apiURL, requests := releaseServer(t, archive, checksumLine(archive, "termlinks_0.6.0_test_test.tar.gz"))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "termlinks")
	writeExecutable(t, executable, []byte("old executable"))

	result, err := Update(context.Background(), Options{CurrentVersion: "0.6.0", ExecutablePath: executable, GOOS: "test", GOARCH: "test", Client: server.Client(), APIURL: apiURL})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated {
		t.Fatal("expected no update")
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("got %d requests, want only the release request", got)
	}
}

func TestUpdateRejectsUntrustedAssetHost(t *testing.T) {
	release := release{TagName: "v0.6.0", Assets: []asset{{Name: "checksums.txt", URL: "https://example.com/checksums.txt"}, {Name: "termlinks_0.6.0_test_test.tar.gz", URL: "https://example.com/archive"}}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(response).Encode(release)
	}))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "termlinks")
	writeExecutable(t, executable, []byte("old executable"))

	_, err := Update(context.Background(), Options{CurrentVersion: "0.5.0", ExecutablePath: executable, GOOS: "test", GOARCH: "test", Client: server.Client(), APIURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "untrusted download host") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func releaseServer(t *testing.T, archive []byte, checksums string) (*httptest.Server, string, *int32) {
	t.Helper()
	var requests int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&requests, 1)
		switch request.URL.Path {
		case "/latest":
			root := "https://" + request.Host
			payload := release{TagName: "v0.6.0", Assets: []asset{
				{Name: "checksums.txt", URL: root + "/checksums.txt"},
				{Name: "termlinks_0.6.0_test_test.tar.gz", URL: root + "/archive.tar.gz"},
			}}
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(payload)
		case "/checksums.txt":
			_, _ = response.Write([]byte(checksums))
		case "/archive.tar.gz":
			_, _ = response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	return server, server.URL + "/latest", &requests
}

func makeArchive(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func checksumLine(contents []byte, filename string) string {
	sum := sha256.Sum256(contents)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filename)
}

func writeExecutable(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o755); err != nil {
		t.Fatal(err)
	}
}
