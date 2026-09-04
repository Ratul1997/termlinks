package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/client"
	"termlinks/backend/internal/cloud"
	"termlinks/backend/internal/cloudflarepages"
	"termlinks/backend/internal/config"
	"termlinks/backend/internal/coordinator"
	"termlinks/backend/internal/selfupdate"
	"termlinks/backend/internal/server"
	"termlinks/backend/internal/session"
	"termlinks/backend/internal/terminalhistory"
	"termlinks/backend/internal/visibleterminal"
	"termlinks/backend/internal/windowcapture"
)

const version = "0.8.5"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "termlinks:", err)
		var status exitStatus
		if errors.As(err, &status) {
			os.Exit(processExitCode(status.code))
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "__agent-stdio":
			return runAgentStdio(args[1:])
		case "__cloudflare-pages-deploy":
			return runCloudflarePagesDeploy(args[1:])
		case "daemon", "serve":
			return runDaemon(args[1:])
		case "list", "ls":
			return listSessions(args[1:])
		case "attach":
			if len(args) != 2 {
				return errors.New("usage: termlinks attach <session-id>")
			}
			return attachSession(args[1])
		case "stop":
			if len(args) != 2 {
				return errors.New("usage: termlinks stop <session-id>")
			}
			return stopSession(args[1])
		case "token":
			return printToken()
		case "doctor":
			return doctor()
		case "cloud":
			return runCloud(args[1:])
		case "desktop":
			return runDesktop(args[1:])
		case "update":
			return runUpdate(args[1:])
		case "version", "--version", "-v":
			fmt.Println("termlinks", version)
			return nil
		case "help", "--help", "-h":
			printHelp()
			return nil
		}
	}
	return runCommand(args)
}

func runAgentStdio(args []string) error {
	if len(args) != 2 || args[0] != "claude" || strings.TrimSpace(args[1]) == "" {
		return errors.New("invalid internal agent runner invocation")
	}
	const maximumPromptBytes = 128 << 10
	reader := bufio.NewReader(os.Stdin)
	header, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read agent prompt header: %w", err)
	}
	fields := strings.Fields(header)
	if len(fields) != 2 || fields[0] != "TERMLINKS_AGENT_PROMPT_V1" {
		return errors.New("invalid internal agent prompt header")
	}
	promptLength, err := strconv.Atoi(fields[1])
	if err != nil || promptLength < 1 || promptLength > maximumPromptBytes {
		return errors.New("agent prompt is empty or too large")
	}
	prompt := make([]byte, promptLength)
	if _, err := io.ReadFull(reader, prompt); err != nil {
		return fmt.Errorf("read agent prompt: %w", err)
	}
	command := exec.Command(args[1], "--print", "--verbose", "--output-format", "stream-json", "--permission-mode", "auto")
	command.Stdin = bytes.NewReader(prompt)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func runUpdate(args []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	localOnly := flags.Bool("local-only", false, "skip optional Cloudflare Pages deployment")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: termlinks update [--local-only]")
	}
	connectorWasRunning := false
	if paths, err := config.ResolvePaths(); err == nil {
		if pid, running := cloudProcess(paths); running && isTermlinksConnector(pid) {
			connectorWasRunning = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := selfupdate.Update(ctx, selfupdate.Options{CurrentVersion: version})
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if result.Updated {
		fmt.Printf("Updated Termlinks %s -> %s using %s (SHA-256 verified).\n", result.From, result.To, result.AssetName)
	} else {
		fmt.Printf("Termlinks %s is already the newest release.\n", result.From)
	}
	if result.Updated && connectorWasRunning {
		fmt.Println("Restarting the cloud connector; active terminal sessions will stay running...")
		if err := stopCloud(); err != nil {
			return fmt.Errorf("update installed, but cloud connector restart could not stop the old process: %w", err)
		}
		if err := startCloud(); err != nil {
			return fmt.Errorf("update installed, but cloud connector restart failed: %w", err)
		}
	}
	if result.Updated {
		fmt.Println("Active terminal sessions and the running daemon were not restarted.")
	}
	if *localOnly {
		fmt.Println("Cloudflare Pages deployment skipped (--local-only).")
		return nil
	}
	config, configured, configErr := cloudflarepages.FromEnvironment()
	if configErr != nil {
		return fmt.Errorf("local update finished, but Cloudflare Pages configuration is invalid: %w", configErr)
	}
	if !configured {
		fmt.Printf("Cloudflare Pages not configured; set %s and %s to include it in future updates.\n", cloudflarepages.TokenEnv, cloudflarepages.AccountEnv)
		return nil
	}
	fmt.Printf("Cloudflare Pages configuration found; deploying project %q from the bundled portal...\n", config.Project)
	deployContext, deployCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer deployCancel()
	if result.Updated {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			return fmt.Errorf("local update finished, but the updated executable could not be located: %w", executableErr)
		}
		command := exec.CommandContext(deployContext, executable, "__cloudflare-pages-deploy")
		command.Stdin = nil
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if commandErr := command.Run(); commandErr != nil {
			return fmt.Errorf("local update finished, but the updated executable could not deploy Cloudflare Pages: %w", commandErr)
		}
		return nil
	}
	if err := cloudflarepages.Deploy(deployContext, config, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("local update finished, but Cloudflare Pages deployment failed: %w", err)
	}
	return nil
}

func runCloudflarePagesDeploy(args []string) error {
	if len(args) != 0 {
		return errors.New("invalid internal Cloudflare Pages deploy invocation")
	}
	config, configured, err := cloudflarepages.FromEnvironment()
	if err != nil {
		return err
	}
	if !configured {
		return errors.New("Cloudflare Pages deployment is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return cloudflarepages.Deploy(ctx, config, os.Stdout, os.Stderr)
}

func runCloud(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printCloudHelp()
		return nil
	}
	switch args[0] {
	case "configure":
		return configureCloud(args[1:])
	case "start":
		return startCloud()
	case "connect":
		return connectCloud()
	case "status":
		return cloudStatus()
	case "stop":
		return stopCloud()
	default:
		return errors.New("usage: termlinks cloud <configure|start|status|stop>")
	}
}

func configureCloud(args []string) error {
	flags := flag.NewFlagSet("cloud configure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	relayURL := flags.String("url", "", "Cloudflare relay URL")
	tokenStdin := flags.Bool("token-stdin", false, "read the connector secret from standard input")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*relayURL) == "" || !*tokenStdin {
		return errors.New("usage: termlinks cloud configure --url https://<relay>.workers.dev --token-stdin")
	}
	token, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil {
		return fmt.Errorf("read connector token: %w", err)
	}
	if len(token) > 4096 {
		return errors.New("connector token is too large")
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	if err := config.Ensure(paths); err != nil {
		return err
	}
	settings := config.CloudSettings{RelayURL: *relayURL, ConnectorToken: strings.TrimSpace(string(token)), VNCAddress: config.DefaultVNCAddress}
	if previous, loadErr := config.LoadCloudSettings(paths); loadErr == nil {
		settings.DesktopEnabled = previous.DesktopEnabled
		settings.VNCAddress = previous.VNCAddress
	}
	if err := config.SaveCloudSettings(paths, settings); err != nil {
		return err
	}
	fmt.Println("Cloud access configured. Start it with: termlinks cloud start")
	return nil
}

func runDesktop(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printDesktopHelp()
		return nil
	}
	switch args[0] {
	case "enable":
		flags := flag.NewFlagSet("desktop enable", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		address := flags.String("address", config.DefaultVNCAddress, "loopback VNC server address")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: termlinks desktop enable [--address 127.0.0.1:5900]")
		}
		return setDesktopEnabled(true, *address)
	case "disable":
		if len(args) != 1 {
			return errors.New("usage: termlinks desktop disable")
		}
		return setDesktopEnabled(false, "")
	case "status":
		if len(args) != 1 {
			return errors.New("usage: termlinks desktop status")
		}
		return desktopStatus()
	case "permissions":
		if len(args) != 1 {
			return errors.New("usage: termlinks desktop permissions")
		}
		return requestDesktopPermissions()
	case "windows":
		if len(args) != 1 {
			return errors.New("usage: termlinks desktop windows")
		}
		return listDesktopWindows()
	default:
		return errors.New("usage: termlinks desktop <enable|disable|status|permissions|windows>")
	}
}

func setDesktopEnabled(enabled bool, address string) error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	settings, err := config.LoadCloudSettings(paths)
	if err != nil {
		return err
	}
	wasRunning := false
	if pid, running := cloudProcess(paths); running && isTermlinksConnector(pid) {
		wasRunning = true
	}
	if enabled {
		address = strings.TrimSpace(address)
		if err := config.ValidateVNCAddress(address); err != nil {
			return err
		}
		settings.DesktopEnabled = true
		settings.VNCAddress = address
	} else {
		settings.DesktopEnabled = false
	}
	if err := config.SaveCloudSettings(paths, settings); err != nil {
		return err
	}
	if wasRunning {
		if err := stopCloud(); err != nil {
			return fmt.Errorf("desktop setting was saved, but the cloud connector could not stop: %w", err)
		}
		if err := startCloud(); err != nil {
			return fmt.Errorf("desktop setting was saved, but the cloud connector could not restart: %w", err)
		}
	}
	if enabled {
		fmt.Printf("Remote desktop tunnel enabled for %s.\n", settings.VNCAddress)
		fmt.Println("Screen Sharing must also be enabled locally in macOS System Settings.")
	} else {
		fmt.Println("Remote desktop tunnel disabled.")
	}
	return nil
}

func desktopStatus() error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	settings, err := config.LoadCloudSettings(paths)
	if err != nil {
		return err
	}
	status := "disabled"
	if settings.DesktopEnabled {
		status = "enabled"
	}
	server := "not checked"
	if settings.DesktopEnabled {
		connection, dialErr := net.DialTimeout("tcp", settings.VNCAddress, 750*time.Millisecond)
		if dialErr == nil {
			server = "reachable"
			_ = connection.Close()
		} else {
			server = "unreachable (enable macOS Screen Sharing and VNC access)"
		}
	}
	permissions := windowcapture.PermissionStatus()
	windowSupport := "unavailable (requires macOS 14+ with cgo)"
	if permissions.Supported {
		windowSupport = "available"
	}
	fmt.Printf("Tunnel:            %s\nVNC target:         %s\nVNC server:         %s\nWindow picker:      %s\nScreen Recording:   %s\nAccessibility:      %s\n",
		status, settings.VNCAddress, server, windowSupport, permissionLabel(permissions.ScreenRecording), permissionLabel(permissions.Accessibility))
	return nil
}

func requestDesktopPermissions() error {
	permissions := windowcapture.RequestPermissions()
	if !permissions.Supported {
		return windowcapture.ErrUnsupported
	}
	fmt.Println("macOS permission requests opened. Approve Termlinks in Privacy & Security if prompted.")
	fmt.Printf("Screen Recording: %s\nAccessibility:    %s\n", permissionLabel(permissions.ScreenRecording), permissionLabel(permissions.Accessibility))
	if !permissions.ScreenRecording || !permissions.Accessibility {
		fmt.Println("After approving, restart the cloud connector with: termlinks cloud stop && termlinks cloud start")
	}
	return nil
}

func permissionLabel(allowed bool) string {
	if allowed {
		return "allowed"
	}
	return "not allowed"
}

func listDesktopWindows() error {
	windows, err := windowcapture.List()
	if err != nil {
		return err
	}
	if len(windows) == 0 {
		fmt.Println("No shareable on-screen windows found.")
		return nil
	}
	for _, window := range windows {
		fmt.Printf("%-10d  %-22s  %4dx%-4d  %s\n", window.ID, truncate(window.Application, 22), window.Width, window.Height, window.Title)
	}
	return nil
}

func startCloud() error {
	paths, err := readyDaemon()
	if err != nil {
		return err
	}
	if _, err := config.LoadCloudSettings(paths); err != nil {
		return err
	}
	if pid, running := cloudProcess(paths); running {
		if isTermlinksConnector(pid) {
			fmt.Printf("Cloud connector is already running (PID %d)\n", pid)
			return nil
		}
		return fmt.Errorf("cloud PID file points to unrelated running process %d; remove %s after verifying it", pid, paths.CloudPID)
	}
	if err := os.Remove(paths.CloudPID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale cloud PID: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(paths.CloudLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := os.Chmod(paths.CloudLog, 0o600); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("secure cloud log: %w", err)
	}
	command := exec.Command(executable, "cloud", "connect")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	if err := os.WriteFile(paths.CloudPID, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o600); err != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("write cloud PID: %w", err)
	}
	if err := os.Chmod(paths.CloudPID, 0o600); err != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("secure cloud PID: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, running := cloudProcess(paths); !running {
		return fmt.Errorf("cloud connector did not start; inspect %s", paths.CloudLog)
	}
	fmt.Println("Cloud connector started. Your computer is now reachable through the portal.")
	return nil
}

func connectCloud() error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cloudSettings, err := config.LoadCloudSettings(paths)
	if err != nil {
		return err
	}
	localSettings, err := config.LoadSettings(paths)
	if err != nil {
		return err
	}
	portalToken, err := config.LoadOrCreateToken(paths)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cloud.Run(ctx, cloudSettings, localSettings.Listen, portalToken, paths.Socket, os.Stderr)
}

func cloudStatus() error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	settings, err := config.LoadCloudSettings(paths)
	if err != nil {
		return err
	}
	pid, running := cloudProcess(paths)
	status := "stopped"
	if running && isTermlinksConnector(pid) {
		status = fmt.Sprintf("running (PID %d)", pid)
	} else if running {
		status = fmt.Sprintf("invalid PID file (unrelated process %d)", pid)
	}
	remote := "offline"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	online, statusErr := cloud.RelayStatus(ctx, settings.RelayURL)
	cancel()
	if statusErr != nil {
		remote = "unreachable"
	} else if online {
		remote = "online"
	}
	fmt.Printf("Portal:    %s\nConnector: %s\nComputer:  %s\n", settings.RelayURL, status, remote)
	return nil
}

func stopCloud() error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	pid, running := cloudProcess(paths)
	if !running {
		if err := os.Remove(paths.CloudPID); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("Cloud connector is already stopped.")
		return nil
	}
	if !isTermlinksConnector(pid) {
		return fmt.Errorf("refusing to signal PID %d because it is not a Termlinks cloud connector", pid)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, alive := cloudProcess(paths); !alive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.Remove(paths.CloudPID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("Cloud connector stopped.")
	return nil
}

func cloudProcess(paths config.Paths) (int, bool) {
	data, err := os.ReadFile(paths.CloudPID)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 2 {
		return 0, false
	}
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		return pid, false
	}
	return pid, true
}

func isTermlinksConnector(pid int) bool {
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	command := string(output)
	return strings.Contains(command, "termlinks") && strings.Contains(command, "cloud connect")
}

func runCommand(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "session name")
	flags.StringVar(name, "n", "", "session name")
	detach := flags.Bool("detach", false, "start without attaching")
	flags.BoolVar(detach, "d", false, "start without attaching")
	port := flags.Int("port", 0, "local web portal port")
	flags.IntVar(port, "p", 0, "local web portal port")
	if err := flags.Parse(args); err != nil {
		return errors.New("usage: termlinks [-p port] [-n name] [-d] [--] <command> [args...]")
	}
	command := flags.Args()
	if len(command) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		command = []string{shell}
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	if flagWasSet(flags, "port", "p") {
		if err := configureDaemonPort(paths, *port); err != nil {
			return err
		}
	}
	paths, err = readyDaemon()
	if err != nil {
		return err
	}
	cols, rows := uint16(100), uint16(30)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		width, height, sizeErr := term.GetSize(int(os.Stdin.Fd()))
		if sizeErr == nil && width >= 20 && height >= 5 && width <= 500 && height <= 300 {
			cols, rows = uint16(width), uint16(height)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	local := client.New(paths.Socket)
	created, err := local.Create(context.Background(), session.StartOptions{
		Name:        *name,
		Command:     command,
		Cwd:         cwd,
		Environment: os.Environ(),
		Cols:        cols,
		Rows:        rows,
	})
	if err != nil {
		return err
	}
	if *detach {
		fmt.Printf("Started %s (%s)\n", created.Name, shortID(created.ID))
		return nil
	}
	result, err := local.Attach(context.Background(), created.ID)
	if err != nil {
		return fmt.Errorf("attach to session %s: %w", shortID(created.ID), err)
	}
	if result.ExitCode != 0 {
		return exitStatus{code: result.ExitCode, signal: result.Signal}
	}
	return nil
}

func runDaemon(args []string) error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	if err := config.Ensure(paths); err != nil {
		return err
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("daemon", flag.ContinueOnError)
	listen := flags.String("listen", settings.Listen, "web listen address")
	port := flags.Int("port", 0, "web listen port")
	flags.IntVar(port, "p", 0, "web listen port")
	allowPublic := flags.Bool("allow-public-bind", false, "allow 0.0.0.0 or [::] binding")
	headless := flags.Bool("headless", false, "do not open native terminal windows for portal-created sessions")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flagWasSet(flags, "port", "p") {
		*listen, err = listenWithPort(*listen, *port)
		if err != nil {
			return err
		}
	}
	if err := validateListen(*listen, *allowPublic); err != nil {
		return err
	}
	if *listen != settings.Listen {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		running := client.New(paths.Socket).Healthy(ctx)
		cancel()
		if running {
			return fmt.Errorf("daemon is already running on %s; changing its listener would stop active sessions", settings.Listen)
		}
	}
	settings.Listen = *listen
	if err := config.SaveSettings(paths, settings); err != nil {
		return err
	}
	token, err := config.LoadOrCreateToken(paths)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	manager := session.NewManager()
	var openVisibleTerminal func(string) error
	if !*headless {
		openVisibleTerminal = visibleterminal.Open
	}
	handlers, err := server.New(manager, auth.New(token), logger, openVisibleTerminal)
	if err != nil {
		return err
	}
	workflowStore, err := coordinator.OpenStore(paths.WorkflowsDB)
	if err != nil {
		return err
	}
	workflowManager := coordinator.NewManager(workflowStore, manager, logger, openVisibleTerminal)
	defer workflowManager.Close()
	handlers.SetCoordinator(workflowManager)
	terminalHistory, err := terminalhistory.Open(paths.TerminalHistoryDB)
	if err != nil {
		return err
	}
	defer terminalHistory.Close()
	handlers.SetTerminalHistory(terminalHistory)
	manager.SetEndObserver(func(info session.Info) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := terminalHistory.RecordEnded(ctx, info); err != nil {
			logger.Warn("could not record completed terminal", "session", info.ID, "error", err)
		}
	})
	go func() {
		refreshContext, refreshCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer refreshCancel()
		if _, err := workflowManager.RefreshAgents(refreshContext); err != nil {
			logger.Warn("could not refresh local AI agents", "error", err)
		}
	}()
	unixListener, err := listenUnix(paths.Socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = unixListener.Close()
		_ = os.Remove(paths.Socket)
	}()
	tcpListener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *listen, err)
	}
	defer tcpListener.Close()

	controlServer := newHTTPServer(handlers.ControlHandler())
	webServer := newHTTPServer(handlers.WebHandler())
	serveErrors := make(chan error, 2)
	go func() { serveErrors <- controlServer.Serve(unixListener) }()
	go func() { serveErrors <- webServer.Serve(tcpListener) }()
	logger.Info("Termlinks is ready", "url", "http://"+tcpListener.Addr().String(), "state", paths.Dir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case serveErr := <-serveErrors:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx, controlServer, webServer)
}

func readyDaemon() (config.Paths, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return config.Paths{}, err
	}
	if err := config.Ensure(paths); err != nil {
		return config.Paths{}, err
	}
	local := client.New(paths.Socket)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	healthy := local.Healthy(ctx)
	cancel()
	if healthy {
		return paths, nil
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		return config.Paths{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return config.Paths{}, err
	}
	logFile, err := os.OpenFile(paths.DaemonLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return config.Paths{}, err
	}
	if err := os.Chmod(paths.DaemonLog, 0o600); err != nil {
		_ = logFile.Close()
		return config.Paths{}, fmt.Errorf("secure daemon log: %w", err)
	}
	command := exec.Command(executable, "daemon", "--listen", settings.Listen)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return config.Paths{}, err
	}
	_ = logFile.Close()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		healthy = local.Healthy(ctx)
		cancel()
		if healthy {
			return paths, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return config.Paths{}, fmt.Errorf("daemon did not start; inspect %s", paths.DaemonLog)
}

func configureDaemonPort(paths config.Paths, port int) error {
	if err := config.Ensure(paths); err != nil {
		return err
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		return err
	}
	desired, err := listenWithPort(settings.Listen, port)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	running := client.New(paths.Socket).Healthy(ctx)
	cancel()
	if running && desired != settings.Listen {
		return fmt.Errorf("daemon is already running on %s; changing to port %d would stop active sessions", settings.Listen, port)
	}
	if desired == settings.Listen {
		return nil
	}
	settings.Listen = desired
	return config.SaveSettings(paths, settings)
}

func listenWithPort(address string, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", errors.New("port must be between 1 and 65535")
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", fmt.Errorf("configured listen address is invalid: %w", err)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func flagWasSet(flags *flag.FlagSet, names ...string) bool {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	found := false
	flags.Visit(func(item *flag.Flag) {
		if _, ok := wanted[item.Name]; ok {
			found = true
		}
	})
	return found
}

func listSessions(args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showAll := flags.Bool("all", false, "include completed sessions retained for scrollback")
	flags.BoolVar(showAll, "a", false, "include completed sessions retained for scrollback")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: termlinks list [--all]")
	}
	paths, err := readyDaemon()
	if err != nil {
		return err
	}
	items, err := client.New(paths.Socket).List(context.Background())
	if err != nil {
		return err
	}
	visible := filterListedSessions(items, *showAll)
	if len(visible) == 0 {
		if *showAll {
			fmt.Println("No sessions. Start one with: termlinks <command>")
		} else {
			fmt.Println("No running sessions. Use --all to include completed sessions.")
		}
		return nil
	}
	for _, item := range visible {
		status := "running"
		if !item.Running {
			status = "exited"
			switch {
			case item.Signal != "":
				status = fmt.Sprintf("killed (%s)", item.Signal)
			case item.ExitCode != nil:
				status = fmt.Sprintf("exited (%d)", *item.ExitCode)
			}
		}
		fmt.Printf("%-10s  %-18s  %-17s  %s\n", shortID(item.ID), truncate(item.Name, 18), status, strings.Join(item.Command, " "))
	}
	return nil
}

func filterListedSessions(items []session.Info, showAll bool) []session.Info {
	visible := make([]session.Info, 0, len(items))
	for _, item := range items {
		if item.Running || showAll {
			visible = append(visible, item)
		}
	}
	return visible
}

func attachSession(id string) error {
	paths, err := readyDaemon()
	if err != nil {
		return err
	}
	items, err := client.New(paths.Socket).List(context.Background())
	if err != nil {
		return err
	}
	resolved, err := resolveID(items, id)
	if err != nil {
		return err
	}
	result, err := client.New(paths.Socket).Attach(context.Background(), resolved)
	if err != nil {
		return err
	}
	if result.AlreadyExited && result.ExitCode == 0 {
		// A successfully completed session is a normal attach outcome. Failed and
		// signal-killed sessions still return their real status below.
		fmt.Fprintf(os.Stderr, "\nSession %s already ended (%s).\n", shortID(resolved), result.Describe())
		return nil
	}
	return attachResultError(result)
}

func stopSession(id string) error {
	paths, err := readyDaemon()
	if err != nil {
		return err
	}
	local := client.New(paths.Socket)
	items, err := local.List(context.Background())
	if err != nil {
		return err
	}
	resolved, err := resolveID(items, id)
	if err != nil {
		return err
	}
	if err := local.Stop(context.Background(), resolved); err != nil {
		return err
	}
	fmt.Println("Stopping", shortID(resolved))
	return nil
}

func printToken() error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	if err := config.Ensure(paths); err != nil {
		return err
	}
	token, err := config.LoadOrCreateToken(paths)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Keep this token private. Enter it only in your Termlinks portal.")
	fmt.Println(token)
	return nil
}

func doctor() error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		return err
	}
	info := map[string]any{
		"version":  version,
		"stateDir": paths.Dir,
		"listen":   settings.Listen,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	info["daemon"] = client.New(paths.Socket).Healthy(ctx)
	cancel()
	encoded, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(encoded))
	return nil
}

func listenUnix(socket string) (net.Listener, error) {
	if connection, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
		_ = connection.Close()
		return nil, errors.New("daemon is already running")
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale control socket: %w", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("create control socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("secure control socket: %w", err)
	}
	return listener, nil
}

func validateListen(address string, allowPublic bool) error {
	resolved, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if allowPublic {
		return nil
	}
	if resolved.IP == nil || resolved.IP.IsUnspecified() || !safePrivateIP(resolved.IP) {
		return errors.New("refusing a public bind; use a specific loopback/private/Tailscale IP or explicitly pass --allow-public-bind")
	}
	return nil
}

func safePrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	tailscaleCGNAT := &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return tailscaleCGNAT.Contains(ip)
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func resolveID(items []session.Info, prefix string) (string, error) {
	var match string
	for _, item := range items {
		if strings.HasPrefix(item.ID, prefix) {
			if match != "" {
				return "", errors.New("session id prefix is ambiguous")
			}
			match = item.ID
		}
	}
	if match == "" {
		return "", errors.New("session not found")
	}
	return match, nil
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

func truncate(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length-1] + "…"
}

type exitStatus struct {
	code   int
	signal string
}

func (e exitStatus) Error() string {
	if e.signal != "" {
		return fmt.Sprintf("command killed by %s", e.signal)
	}
	return fmt.Sprintf("command exited with status %d", e.code)
}

func attachResultError(result client.AttachResult) error {
	if result.ExitCode == 0 {
		return nil
	}
	return exitStatus{code: result.ExitCode, signal: result.Signal}
}

// processExitCode keeps a status os.Exit cannot represent from being truncated
// into a misleading one (on Unix only the low 8 bits survive, so -1 would
// surface as 255 and 256 as success).
func processExitCode(code int) int {
	if code < 0 || code > 255 {
		return 1
	}
	return code
}

func printHelp() {
	fmt.Print(`Termlinks — keep local terminal work reachable from your phone

Usage:
  termlinks [-p port] [-n name] [-d] [--] <command> [args...]
  termlinks                         Start your default shell
  termlinks list                    List running sessions
  termlinks list --all              Include completed sessions
  termlinks attach <id>             Reattach locally
  termlinks stop <id>               Gracefully stop a session
  termlinks token                   Print the private portal login token
  termlinks doctor                  Show safe local diagnostics
  termlinks update [--local-only]   Update locally and optionally deploy configured Pages
  termlinks cloud configure ...     Configure the Cloudflare relay
  termlinks cloud start             Connect this computer to the cloud portal
  termlinks cloud status            Show cloud connector status
  termlinks cloud stop              Disconnect the cloud portal
  termlinks desktop enable          Allow encrypted remote desktop access
  termlinks desktop status          Check the desktop tunnel and VNC server
  termlinks desktop permissions     Request window view/control permissions
  termlinks desktop windows         List shareable Mac windows locally
  termlinks desktop disable         Revoke remote desktop access
  termlinks daemon [-p port] [--listen addr] [--headless]
                                    Run the daemon in the foreground

Examples:
  termlinks codex
  termlinks -p 9000 codex
  termlinks -n api -- npm run dev
  termlinks -d -- python import.py
`)
}

func printDesktopHelp() {
	fmt.Print(`Termlinks remote desktop (macOS-first)

Usage:
  termlinks desktop enable [--address 127.0.0.1:5900]
  termlinks desktop status
  termlinks desktop permissions
  termlinks desktop windows
  termlinks desktop disable

The address must be localhost or a loopback IP. Remote desktop is disabled by
default. Full-desktop mode also needs macOS Screen Sharing. Selected-window mode
needs macOS 14+ plus Screen Recording permission for viewing and Accessibility
permission for control. Request those local permissions with the command above.
`)
}

func printCloudHelp() {
	fmt.Print(`Termlinks cloud access

Usage:
  termlinks cloud configure --url https://<relay>.workers.dev --token-stdin
  termlinks cloud start
  termlinks cloud status
  termlinks cloud stop

The connector token is read from standard input so it does not appear in shell history.
It is different from the portal login token shown by: termlinks token
`)
}
