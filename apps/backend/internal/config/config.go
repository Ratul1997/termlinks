package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultListen     = "127.0.0.1:57321"
	DefaultVNCAddress = "127.0.0.1:5900"
)

type Paths struct {
	Dir               string
	Socket            string
	Token             string
	Settings          string
	DaemonLog         string
	DaemonPID         string
	DaemonUpdate      string
	Cloud             string
	CloudPID          string
	CloudLog          string
	WorkflowsDB       string
	TerminalHistoryDB string
	WorkflowArtifacts string
	WorkflowWorktrees string
}

type Settings struct {
	Listen string `json:"listen"`
}

type CloudSettings struct {
	RelayURL       string `json:"relayUrl"`
	ConnectorToken string `json:"connectorToken"`
	DesktopEnabled bool   `json:"desktopEnabled,omitempty"`
	VNCAddress     string `json:"vncAddress,omitempty"`
}

func ResolvePaths() (Paths, error) {
	dir := strings.TrimSpace(os.Getenv("TERMLINKS_STATE_DIR"))
	if dir != "" {
		if !filepath.IsAbs(dir) {
			return Paths{}, errors.New("TERMLINKS_STATE_DIR must be an absolute path")
		}
	} else {
		base, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		dir = filepath.Join(base, "termlinks")
	}
	dir = filepath.Clean(dir)
	return Paths{
		Dir:               dir,
		Socket:            filepath.Join(dir, "control.sock"),
		Token:             filepath.Join(dir, "auth.token"),
		Settings:          filepath.Join(dir, "settings.json"),
		DaemonLog:         filepath.Join(dir, "daemon.log"),
		DaemonPID:         filepath.Join(dir, "daemon.pid"),
		DaemonUpdate:      filepath.Join(dir, "daemon-update-pending"),
		Cloud:             filepath.Join(dir, "cloud.json"),
		CloudPID:          filepath.Join(dir, "cloud.pid"),
		CloudLog:          filepath.Join(dir, "cloud.log"),
		WorkflowsDB:       filepath.Join(dir, "workflows.db"),
		TerminalHistoryDB: filepath.Join(dir, "terminal-history.db"),
		WorkflowArtifacts: filepath.Join(dir, "workflow-artifacts"),
		WorkflowWorktrees: filepath.Join(dir, "workflow-worktrees"),
	}, nil
}

func LoadCloudSettings(paths Paths) (CloudSettings, error) {
	data, err := os.ReadFile(paths.Cloud)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CloudSettings{}, errors.New("cloud access is not configured; run: termlinks cloud configure --url <relay-url> --token-stdin")
		}
		return CloudSettings{}, fmt.Errorf("read cloud settings: %w", err)
	}
	if err := os.Chmod(paths.Cloud, 0o600); err != nil {
		return CloudSettings{}, fmt.Errorf("secure cloud settings: %w", err)
	}
	var settings CloudSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return CloudSettings{}, fmt.Errorf("parse cloud settings: %w", err)
	}
	if strings.TrimSpace(settings.VNCAddress) == "" {
		settings.VNCAddress = DefaultVNCAddress
	}
	if err := ValidateCloudSettings(settings); err != nil {
		return CloudSettings{}, err
	}
	return settings, nil
}

func SaveCloudSettings(paths Paths, settings CloudSettings) error {
	settings.RelayURL = strings.TrimRight(strings.TrimSpace(settings.RelayURL), "/")
	settings.ConnectorToken = strings.TrimSpace(settings.ConnectorToken)
	settings.VNCAddress = strings.TrimSpace(settings.VNCAddress)
	if settings.VNCAddress == "" {
		settings.VNCAddress = DefaultVNCAddress
	}
	if err := ValidateCloudSettings(settings); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cloud settings: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(paths.Dir, ".cloud-*.tmp")
	if err != nil {
		return fmt.Errorf("create cloud settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure cloud settings: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write cloud settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close cloud settings: %w", err)
	}
	if err := os.Rename(temporaryPath, paths.Cloud); err != nil {
		return fmt.Errorf("save cloud settings: %w", err)
	}
	return os.Chmod(paths.Cloud, 0o600)
}

func ValidateCloudSettings(settings CloudSettings) error {
	parsed, err := url.Parse(strings.TrimSpace(settings.RelayURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("relay URL must be a plain https:// URL without credentials, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("relay URL must not include a path")
	}
	if len(strings.TrimSpace(settings.ConnectorToken)) < 32 {
		return errors.New("connector token must contain at least 32 characters")
	}
	if settings.DesktopEnabled {
		if err := ValidateVNCAddress(settings.VNCAddress); err != nil {
			return err
		}
	}
	return nil
}

func ValidateVNCAddress(address string) error {
	host, portValue, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return errors.New("VNC address must be a loopback host and port, for example 127.0.0.1:5900")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("VNC port is invalid")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("VNC address must use localhost or a loopback IP")
	}
	return nil
}

func Ensure(paths Paths) error {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}
	for _, directory := range []string{paths.WorkflowArtifacts, paths.WorkflowWorktrees} {
		if directory == "" {
			continue
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create private workflow directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure private workflow directory: %w", err)
		}
	}
	return nil
}

func LoadSettings(paths Paths) (Settings, error) {
	settings := Settings{Listen: DefaultListen}
	data, err := os.ReadFile(paths.Settings)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	if strings.TrimSpace(settings.Listen) == "" {
		settings.Listen = DefaultListen
	}
	return settings, nil
}

func SaveSettings(paths Paths, settings Settings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(paths.Settings, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return os.Chmod(paths.Settings, 0o600)
}

func LoadOrCreateToken(paths Paths) (string, error) {
	data, err := os.ReadFile(paths.Token)
	if err == nil {
		if err := os.Chmod(paths.Token, 0o600); err != nil {
			return "", fmt.Errorf("secure token file: %w", err)
		}
		token := strings.TrimSpace(string(data))
		if len(token) < 32 {
			return "", errors.New("stored authentication token is invalid")
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read authentication token: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate authentication token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	file, err := os.OpenFile(paths.Token, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateToken(paths)
	}
	if err != nil {
		return "", fmt.Errorf("create authentication token: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write authentication token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close authentication token: %w", err)
	}
	return token, nil
}
