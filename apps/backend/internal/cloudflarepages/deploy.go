package cloudflarepages

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"termlinks/backend/internal/webui"
)

const (
	TokenEnv        = "TERMLINKS_CLOUDFLARE_API_TOKEN"
	AccountEnv      = "TERMLINKS_CLOUDFLARE_ACCOUNT_ID"
	ProjectEnv      = "TERMLINKS_CLOUDFLARE_PAGES_PROJECT"
	BranchEnv       = "TERMLINKS_CLOUDFLARE_PAGES_BRANCH"
	defaultProject  = "termlinks"
	defaultBranch   = "main"
	wranglerPackage = "wrangler@4.129.0"
)

var (
	accountPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	projectPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,56}[a-z0-9])?$`)
	branchPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
)

//go:embed all:bundle
var deploymentFiles embed.FS

type Config struct {
	APIToken  string
	AccountID string
	Project   string
	Branch    string
}

func FromEnvironment() (Config, bool, error) {
	return fromLookup(os.LookupEnv)
}

func fromLookup(lookup func(string) (string, bool)) (Config, bool, error) {
	values := make(map[string]string, 4)
	configured := false
	for _, name := range []string{TokenEnv, AccountEnv, ProjectEnv, BranchEnv} {
		if value, ok := lookup(name); ok {
			configured = true
			values[name] = strings.TrimSpace(value)
		}
	}
	if !configured {
		return Config{}, false, nil
	}
	config := Config{
		APIToken:  values[TokenEnv],
		AccountID: values[AccountEnv],
		Project:   values[ProjectEnv],
		Branch:    values[BranchEnv],
	}
	if config.Project == "" {
		config.Project = defaultProject
	}
	if config.Branch == "" {
		config.Branch = defaultBranch
	}
	if err := config.validate(); err != nil {
		return Config{}, true, err
	}
	return config, true, nil
}

func (config Config) validate() error {
	if len(config.APIToken) < 20 || strings.IndexFunc(config.APIToken, func(character rune) bool {
		return character <= 0x20 || character == 0x7f
	}) >= 0 {
		return fmt.Errorf("%s must contain a valid Cloudflare API token", TokenEnv)
	}
	if !accountPattern.MatchString(config.AccountID) {
		return fmt.Errorf("%s must be a 32-character hexadecimal account ID", AccountEnv)
	}
	if !projectPattern.MatchString(config.Project) {
		return fmt.Errorf("%s must use lowercase letters, numbers, or hyphens", ProjectEnv)
	}
	if !branchPattern.MatchString(config.Branch) || strings.Contains(config.Branch, "..") || strings.Contains(config.Branch, "//") {
		return fmt.Errorf("%s contains an invalid branch name", BranchEnv)
	}
	return nil
}

func Deploy(ctx context.Context, config Config, stdout, stderr io.Writer) error {
	return deploy(ctx, config, webui.Files, deploymentFiles, stdout, stderr, exec.LookPath, exec.CommandContext)
}

type commandFactory func(context.Context, string, ...string) *exec.Cmd

func deploy(
	ctx context.Context,
	config Config,
	portalFiles fs.FS,
	functionFiles fs.FS,
	stdout io.Writer,
	stderr io.Writer,
	lookPath func(string) (string, error),
	command commandFactory,
) error {
	if err := config.validate(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "termlinks-pages-*")
	if err != nil {
		return fmt.Errorf("create Pages deployment directory: %w", err)
	}
	defer os.RemoveAll(root)
	if err := copyTree(portalFiles, "dist", filepath.Join(root, "dist")); err != nil {
		return fmt.Errorf("stage bundled portal: %w", err)
	}
	if err := copyTree(functionFiles, "bundle/functions", filepath.Join(root, "functions")); err != nil {
		return fmt.Errorf("stage bundled Pages Function: %w", err)
	}

	executable, arguments, err := wranglerCommand(lookPath)
	if err != nil {
		return err
	}
	arguments = append(arguments,
		"pages", "deploy", "dist",
		"--project-name", config.Project,
		"--branch", config.Branch,
		"--commit-dirty=false",
	)
	process := command(ctx, executable, arguments...)
	process.Dir = root
	process.Env = deploymentEnvironment(os.Environ(), config)
	process.Stdin = nil
	process.Stdout = stdout
	process.Stderr = stderr
	if err := process.Run(); err != nil {
		return fmt.Errorf("Cloudflare Pages deployment failed: %w", err)
	}
	return nil
}

func wranglerCommand(lookPath func(string) (string, error)) (string, []string, error) {
	if executable, err := lookPath("wrangler"); err == nil {
		return executable, nil, nil
	}
	if executable, err := lookPath("npx"); err == nil {
		return executable, []string{"--yes", wranglerPackage}, nil
	}
	return "", nil, errors.New("Cloudflare Pages deployment requires Wrangler or Node.js with npx")
}

func deploymentEnvironment(base []string, config Config) []string {
	blocked := map[string]bool{
		TokenEnv: true, AccountEnv: true,
		"CLOUDFLARE_API_TOKEN": true, "CLOUDFLARE_ACCOUNT_ID": true,
	}
	output := make([]string, 0, len(base)+2)
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		if !blocked[name] {
			output = append(output, item)
		}
	}
	return append(output,
		"CLOUDFLARE_API_TOKEN="+config.APIToken,
		"CLOUDFLARE_ACCOUNT_ID="+config.AccountID,
	)
}

func copyTree(source fs.FS, sourceRoot, destinationRoot string) error {
	return fs.WalkDir(source, sourceRoot, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !fs.ValidPath(name) || (name != sourceRoot && !strings.HasPrefix(name, sourceRoot+"/")) {
			return errors.New("bundled deployment contains an unsafe path")
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(name, sourceRoot), "/")
		if relative == "" {
			relative = "."
		}
		destination := filepath.Join(destinationRoot, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("bundled deployment contains unsupported file %q", name)
		}
		return copyFile(source, name, destination)
	})
}

func copyFile(source fs.FS, name, destination string) error {
	input, err := source.Open(name)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	inputCloseErr := input.Close()
	syncErr := output.Sync()
	outputCloseErr := output.Close()
	for _, candidate := range []error{copyErr, inputCloseErr, syncErr, outputCloseErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}
