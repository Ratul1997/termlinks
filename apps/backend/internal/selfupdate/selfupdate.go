package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	LatestReleaseURL = "https://api.github.com/repos/Ratul1997/termlinks/releases/latest"
	maxReleaseBytes  = 1 << 20
	maxChecksumBytes = 1 << 20
	maxArchiveBytes  = 128 << 20
	maxBinaryBytes   = 128 << 20
)

type Options struct {
	CurrentVersion string
	ExecutablePath string
	GOOS           string
	GOARCH         string
	Client         *http.Client
	APIURL         string
}

type Result struct {
	From      string
	To        string
	AssetName string
	Updated   bool
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func Update(ctx context.Context, options Options) (Result, error) {
	if options.CurrentVersion == "" {
		return Result{}, errors.New("current version is required")
	}
	current, err := parseVersion(options.CurrentVersion)
	if err != nil {
		return Result{}, fmt.Errorf("current version: %w", err)
	}
	if options.ExecutablePath == "" {
		options.ExecutablePath, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("locate executable: %w", err)
		}
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.APIURL == "" {
		options.APIURL = LatestReleaseURL
	}

	client, err := secureClient(options.Client, options.APIURL)
	if err != nil {
		return Result{}, err
	}
	var latest release
	apiURL, _ := url.Parse(options.APIURL)
	trustedAPIHost := strings.ToLower(apiURL.Hostname())
	if err := getJSON(ctx, client, options.APIURL, trustedAPIHost, maxReleaseBytes, &latest); err != nil {
		return Result{}, fmt.Errorf("check latest release: %w", err)
	}
	latestVersion, err := parseVersion(latest.TagName)
	if err != nil {
		return Result{}, fmt.Errorf("latest release tag: %w", err)
	}
	result := Result{From: current.String(), To: latestVersion.String()}
	if current.Compare(latestVersion) >= 0 {
		return result, nil
	}

	assetName := fmt.Sprintf("termlinks_%s_%s_%s.tar.gz", latestVersion.String(), options.GOOS, options.GOARCH)
	archiveAsset, ok := findAsset(latest.Assets, assetName)
	if !ok {
		return Result{}, fmt.Errorf("release %s has no build for %s/%s", latestVersion.String(), options.GOOS, options.GOARCH)
	}
	checksumsAsset, ok := findAsset(latest.Assets, "checksums.txt")
	if !ok {
		return Result{}, fmt.Errorf("release %s has no checksums.txt", latestVersion.String())
	}

	checksums, err := getBytes(ctx, client, checksumsAsset.URL, trustedAPIHost, maxChecksumBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums: %w", err)
	}
	expectedChecksum, err := checksumFor(checksums, assetName)
	if err != nil {
		return Result{}, err
	}

	tempDir, err := os.MkdirTemp("", "termlinks-update-*")
	if err != nil {
		return Result{}, fmt.Errorf("create update workspace: %w", err)
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, assetName)
	actualChecksum, err := downloadFile(ctx, client, archiveAsset.URL, archivePath, trustedAPIHost, maxArchiveBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", assetName, err)
	}
	if subtle.ConstantTimeCompare(expectedChecksum, actualChecksum) != 1 {
		return Result{}, fmt.Errorf("SHA-256 verification failed for %s", assetName)
	}

	candidatePath := filepath.Join(tempDir, "termlinks")
	if err := extractBinary(archivePath, candidatePath); err != nil {
		return Result{}, fmt.Errorf("extract %s: %w", assetName, err)
	}
	if err := verifyCandidate(ctx, candidatePath, latestVersion.String()); err != nil {
		return Result{}, err
	}
	if err := replaceExecutable(options.ExecutablePath, candidatePath); err != nil {
		return Result{}, err
	}

	result.AssetName = assetName
	result.Updated = true
	return result, nil
}

func secureClient(base *http.Client, apiURL string) (*http.Client, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("release API must use an HTTPS URL without credentials")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	if base != nil {
		*client = *base
		if client.Timeout == 0 {
			client.Timeout = 2 * time.Minute
		}
	}
	apiHost := strings.ToLower(parsed.Hostname())
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return validateDownloadURL(request.URL, apiHost)
	}
	return client, nil
}

func validateDownloadURL(value *url.URL, apiHost string) error {
	if value.Scheme != "https" || value.Hostname() == "" || value.User != nil {
		return errors.New("download URL must use HTTPS without credentials")
	}
	host := strings.ToLower(value.Hostname())
	if host == apiHost || host == "api.github.com" || host == "github.com" || host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return nil
	}
	return fmt.Errorf("refusing untrusted download host %q", host)
}

func newRequest(ctx context.Context, rawURL string, apiHost string) (*http.Request, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateDownloadURL(parsed, apiHost); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "termlinks-self-update")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return request, nil
}

func getJSON(ctx context.Context, client *http.Client, rawURL, apiHost string, limit int64, target any) error {
	data, err := getBytes(ctx, client, rawURL, apiHost, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func getBytes(ctx context.Context, client *http.Client, rawURL, apiHost string, limit int64) ([]byte, error) {
	request, err := newRequest(ctx, rawURL, apiHost)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > limit {
		return nil, errors.New("response is too large")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response is too large")
	}
	return data, nil
}

func downloadFile(ctx context.Context, client *http.Client, rawURL, destination, apiHost string, limit int64) ([]byte, error) {
	request, err := newRequest(ctx, rawURL, apiHost)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > limit {
		return nil, errors.New("download is too large")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, limit+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if written > limit {
		return nil, errors.New("download is too large")
	}
	if syncErr != nil {
		return nil, syncErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return hash.Sum(nil), nil
}

func checksumFor(contents []byte, filename string) ([]byte, error) {
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		checksum, err := hex.DecodeString(fields[0])
		if err != nil || len(checksum) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA-256 entry for %s", filename)
		}
		return checksum, nil
	}
	return nil, fmt.Errorf("checksums.txt has no SHA-256 entry for %s", filename)
}

func extractBinary(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	found := false
	for entries := 0; ; entries++ {
		if entries >= 32 {
			return errors.New("archive contains too many entries")
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		cleanName := filepath.Clean(header.Name)
		if filepath.IsAbs(header.Name) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
			return errors.New("archive contains an unsafe path")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("archive contains unsupported entry %q", header.Name)
		}
		if cleanName != "termlinks" || found {
			return fmt.Errorf("archive contains unexpected entry %q", header.Name)
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(tarReader, maxBinaryBytes+1))
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if written > maxBinaryBytes {
			return errors.New("binary is too large")
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		found = true
	}
	if !found {
		return errors.New("archive does not contain termlinks")
	}
	return nil
}

func verifyCandidate(ctx context.Context, candidatePath, expectedVersion string) error {
	verifyContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(verifyContext, candidatePath, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify downloaded executable: %w", err)
	}
	if strings.TrimSpace(string(output)) != "termlinks "+expectedVersion {
		return fmt.Errorf("downloaded executable reported unexpected version %q", strings.TrimSpace(string(output)))
	}
	return nil
}

func replaceExecutable(executablePath, candidatePath string) error {
	executablePath, err := filepath.Abs(executablePath)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("current executable is not a regular file")
	}
	staging, err := os.CreateTemp(filepath.Dir(executablePath), ".termlinks-update-*")
	if err != nil {
		return fmt.Errorf("prepare update beside %s: %w (if it is administrator-owned, rerun with appropriate privileges)", executablePath, err)
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath)
	candidate, err := os.Open(candidatePath)
	if err != nil {
		_ = staging.Close()
		return err
	}
	_, copyErr := io.Copy(staging, candidate)
	closeCandidateErr := candidate.Close()
	chmodErr := staging.Chmod(0o755)
	syncErr := staging.Sync()
	closeErr := staging.Close()
	if copyErr != nil {
		return fmt.Errorf("stage update: %w", copyErr)
	}
	if closeCandidateErr != nil {
		return fmt.Errorf("read downloaded executable: %w", closeCandidateErr)
	}
	if chmodErr != nil {
		return fmt.Errorf("make staged update executable: %w", chmodErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync staged update: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged update: %w", closeErr)
	}
	if err := os.Rename(stagingPath, executablePath); err != nil {
		return fmt.Errorf("replace %s: %w (if it is administrator-owned, rerun with appropriate privileges)", executablePath, err)
	}
	directory, err := os.Open(filepath.Dir(executablePath))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func findAsset(assets []asset, name string) (asset, bool) {
	for _, candidate := range assets {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return asset{}, false
}

type semanticVersion [3]uint64

func parseVersion(value string) (semanticVersion, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("%q is not a stable semantic version", value)
	}
	var parsed semanticVersion
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, fmt.Errorf("%q is not a stable semantic version", value)
		}
		number, err := strconv.ParseUint(part, 10, 63)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("%q is not a stable semantic version", value)
		}
		parsed[index] = number
	}
	return parsed, nil
}

func (version semanticVersion) Compare(other semanticVersion) int {
	for index := range version {
		if version[index] < other[index] {
			return -1
		}
		if version[index] > other[index] {
			return 1
		}
	}
	return 0
}

func (version semanticVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
}
