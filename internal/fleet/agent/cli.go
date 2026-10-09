package agent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

const cliTimeout = 30 * time.Second

var (
	cliNewManager = func(getenv func(string) string) ServiceManager {
		return NewServiceManager(runtime.GOOS, nil, getenv, mustHomeDir())
	}
	cliRuntimeFactory = func(store *Store, dataDir string, prober *Prober, manager ServiceManager) (*Runtime, error) {
		return New(Options{Store: store, DataDir: dataDir, Prober: prober, Manager: manager})
	}
)

// RunCLI executes the device agent subcommands (enroll, run, install,
// uninstall, status) and returns the process exit code. The same-binary
// wiring (for example `nexterm-desktop agent ...`) calls this entrypoint.
func RunCLI(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printCLIUsage(stderr)
		return ExitUsage
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	command, rest := args[0], args[1:]
	switch command {
	case "enroll":
		return cliEnroll(rest, getenv, stdout, stderr)
	case "run":
		return cliRun(rest, getenv, stdout, stderr)
	case "install":
		return cliInstall(rest, getenv, stdout, stderr)
	case "uninstall":
		return cliUninstall(rest, getenv, stdout, stderr)
	case "status":
		return cliStatus(rest, getenv, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "agent: 未知子命令 %q\n", command)
		printCLIUsage(stderr)
		return ExitUsage
	}
}

func printCLIUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, "用法: agent <enroll|run|install|uninstall|status> [选项]")
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	return set
}

func resolveDataDir(flagValue string, getenv func(string) string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if value := getenv("NEXTERM_DATA_DIR"); value != "" {
		return value, nil
	}
	return "", errors.New("需要 --data-dir 或 NEXTERM_DATA_DIR")
}

func cliEnroll(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	set := newFlagSet("enroll", stderr)
	server := set.String("server", "", "服务器地址 (必填)")
	code := set.String("code", "", "设备注册码 (必填)")
	name := set.String("name", "", "设备名 (默认主机名)")
	dataDir := set.String("data-dir", "", "数据目录 (默认 NEXTERM_DATA_DIR)")
	insecure := set.Bool("insecure", false, "跳过 TLS 证书校验 (仅限自签名)")
	if err := set.Parse(args); err != nil {
		return ExitUsage
	}
	if *server == "" || *code == "" {
		fmt.Fprintln(stderr, "agent enroll: --server 与 --code 必填")
		return ExitUsage
	}
	dir, err := resolveDataDir(*dataDir, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "agent enroll:", err)
		return ExitUsage
	}
	deviceName := *name
	if deviceName == "" {
		if hostname, err := os.Hostname(); err == nil {
			deviceName = hostname
		}
	}
	client, err := NewEndpointClient(BaseURLEntry{URL: *server, Insecure: *insecure})
	if err != nil {
		fmt.Fprintln(stderr, "agent enroll:", err)
		return ExitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	response, err := client.Enroll(ctx, EnrollRequest{
		Code:       *code,
		Name:       deviceName,
		Platform:   runtime.GOOS,
		AppVersion: version.Version,
	})
	if err != nil {
		printServerError(stderr, "注册失败", err)
		return ExitError
	}
	config := &Config{
		Version:           configVersion,
		DeviceID:          response.DeviceID,
		Secret:            response.Secret,
		Name:              deviceName,
		Platform:          runtime.GOOS,
		AppVersion:        version.Version,
		BaseURLs:          response.BaseURLs,
		MetricsIntervalMS: clampMetricsInterval(response.MetricsIntervalMS),
		DesiredAutostart:  response.DesiredAutostart,
		TerminalEnabled:   response.TerminalEnabled,
		EnrolledAt:        ids.NowMS(),
	}
	if len(config.BaseURLs) == 0 {
		fmt.Fprintln(stderr, "agent enroll: 服务器未返回任何接入地址")
		return ExitError
	}
	store := StoreAt(dir)
	if err := store.Save(config); err != nil {
		fmt.Fprintln(stderr, "agent enroll:", err)
		return ExitError
	}
	fmt.Fprintf(stdout, "注册成功: device_id=%s name=%s base_urls=%d metrics_interval=%ds desired_autostart=%t terminal=%s\n",
		config.DeviceID, config.Name, len(config.BaseURLs), config.MetricsIntervalMS/1000, config.DesiredAutostart, enabledText(config.TerminalEnabled))
	fmt.Fprintf(stdout, "配置已写入 %s (0600)\n", store.Path())
	return ExitOK
}

func cliRun(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	set := newFlagSet("run", stderr)
	dataDir := set.String("data-dir", "", "数据目录 (默认 NEXTERM_DATA_DIR)")
	if err := set.Parse(args); err != nil {
		return ExitUsage
	}
	dir, err := resolveDataDir(*dataDir, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitUsage
	}
	if err := os.MkdirAll(filepath.Join(dir, "fleet"), 0o700); err != nil {
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitError
	}
	unlock, err := acquireLock(filepath.Join(dir, "fleet", "agent.lock"))
	if errors.Is(err, errLockBusy) {
		fmt.Fprintln(stdout, "另一个 agent 实例正在运行, 退出")
		return ExitOK
	}
	if err != nil {
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitError
	}
	defer unlock()
	store := StoreAt(dir)
	config, err := store.Load()
	if errors.Is(err, ErrNotEnrolled) {
		fmt.Fprintln(stderr, "agent run: 设备未注册, 请先执行 agent enroll")
		return ExitError
	}
	if err != nil {
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitError
	}
	prober, err := NewProber(config.BaseURLs)
	if err != nil {
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitError
	}
	agentRuntime, err := cliRuntimeFactory(store, dir, prober, cliNewManager(getenv))
	if err != nil {
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "agent 运行中: device_id=%s name=%s\n", config.DeviceID, config.Name)
	err = agentRuntime.Run(ctx)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrRevoked):
		fmt.Fprintln(stderr, "agent: 设备已被吊销, 停止运行; 请重新 enroll 接入新设备")
		return ExitRevoked
	case errors.Is(err, ErrProtocolMismatch):
		fmt.Fprintln(stderr, "agent: 控制通道协议版本不兼容, 请升级本程序")
		return ExitProtocol
	default:
		fmt.Fprintln(stderr, "agent run:", err)
		return ExitError
	}
}

func cliInstall(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return cliServiceChange("install", args, getenv, stdout, stderr, func(ctx context.Context, manager ServiceManager, executable, dataDir string) error {
		return manager.Install(ctx, executable, dataDir)
	})
}

func cliUninstall(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return cliServiceChange("uninstall", args, getenv, stdout, stderr, func(ctx context.Context, manager ServiceManager, _, _ string) error {
		return manager.Uninstall(ctx)
	})
}

func cliServiceChange(command string, args []string, getenv func(string) string, stdout, stderr io.Writer, action func(context.Context, ServiceManager, string, string) error) int {
	set := newFlagSet(command, stderr)
	dataDir := set.String("data-dir", "", "数据目录 (默认 NEXTERM_DATA_DIR)")
	if err := set.Parse(args); err != nil {
		return ExitUsage
	}
	dir, err := resolveDataDir(*dataDir, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "agent "+command+":", err)
		return ExitUsage
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "agent "+command+":", err)
		return ExitError
	}
	manager := cliNewManager(getenv)
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	if err := action(ctx, manager, executable, dir); err != nil {
		fmt.Fprintln(stderr, "agent "+command+":", err)
		return ExitError
	}
	status := manager.Status(ctx)
	fmt.Fprintf(stdout, "%s 完成: installed=%t enabled=%t active=%t\n", command, status.Installed, status.Enabled, status.Active)
	if status.LastError != "" {
		fmt.Fprintf(stdout, "注意: %s\n", status.LastError)
	}
	return ExitOK
}

func cliStatus(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	set := newFlagSet("status", stderr)
	dataDir := set.String("data-dir", "", "数据目录 (默认 NEXTERM_DATA_DIR)")
	if err := set.Parse(args); err != nil {
		return ExitUsage
	}
	dir, err := resolveDataDir(*dataDir, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "agent status:", err)
		return ExitUsage
	}
	store := StoreAt(dir)
	config, err := store.Load()
	if errors.Is(err, ErrNotEnrolled) {
		fmt.Fprintf(stdout, "配置: 未注册 (配置路径 %s)\n", store.Path())
	} else if err != nil {
		fmt.Fprintf(stdout, "配置: 读取失败: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "配置: device_id=%s name=%s platform=%s app_version=%s\n", config.DeviceID, config.Name, config.Platform, config.AppVersion)
		fmt.Fprintf(stdout, "接入地址: 当前=%s 已配置=%d metrics_interval=%ds desired_autostart=%t terminal=%s\n",
			orNone(config.CurrentURL), len(config.BaseURLs), config.MetricsIntervalMS/1000, config.DesiredAutostart, enabledText(config.TerminalEnabled))
	}
	manager := cliNewManager(getenv)
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	status := manager.Status(ctx)
	fmt.Fprintf(stdout, "服务: installed=%t enabled=%t active=%t\n", status.Installed, status.Enabled, status.Active)
	if status.LastError != "" {
		fmt.Fprintf(stdout, "服务错误: %s\n", status.LastError)
	}
	stateDir := filepath.Join(dir, "durable", "supervisor")
	if endpoint, err := supervisor.HelperEndpoint(stateDir); err == nil {
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		_, probeErr := supervisor.NewClient(endpoint, stateDir).List(probeCtx)
		probeCancel()
		if probeErr != nil {
			fmt.Fprintf(stdout, "supervisor helper: 未运行 (%v)\n", probeErr)
		} else {
			fmt.Fprintf(stdout, "supervisor helper: 正常 (%s)\n", endpoint)
		}
	}
	collector := NewCollector(dir, time.Now)
	if _, err := collector.Collect(); err != nil {
		fmt.Fprintf(stdout, "指标采集: 不可用 (%v)\n", err)
	} else {
		fmt.Fprintln(stdout, "指标采集: 正常")
	}
	return ExitOK
}

func printServerError(stderr io.Writer, prefix string, err error) {
	var serverError *ServerError
	if errors.As(err, &serverError) {
		fmt.Fprintf(stderr, "agent: %s: %s\n", prefix, serverError.Message)
		return
	}
	fmt.Fprintf(stderr, "agent: %s: %v\n", prefix, err)
}

func mustHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func enabledText(enabled bool) string {
	if enabled {
		return "开启"
	}
	return "关闭"
}

func orNone(value string) string {
	if value == "" {
		return "无"
	}
	return value
}
