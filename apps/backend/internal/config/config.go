package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultListen = "127.0.0.1:8787"

type Paths struct {
	Dir       string
	Socket    string
	Token     string
	Settings  string
	DaemonLog string
}

type Settings struct {
	Listen string `json:"listen"`
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
		Dir:       dir,
		Socket:    filepath.Join(dir, "control.sock"),
		Token:     filepath.Join(dir, "auth.token"),
		Settings:  filepath.Join(dir, "settings.json"),
		DaemonLog: filepath.Join(dir, "daemon.log"),
	}, nil
}

func Ensure(paths Paths) error {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
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
