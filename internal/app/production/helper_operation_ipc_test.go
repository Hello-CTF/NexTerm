package production

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionHelperProgressEventPreservesOperationEvidence(t *testing.T) {
	deadline := time.Date(2026, time.October, 22, 12, 45, 0, 123000000, time.UTC)
	scope := ipc.HelperProgressScope{SessionID: "session-a", AssetID: "asset-a"}
	progress := sshdaemon.HelperProgress{
		OperationID: "helper-operation-a",
		Attempt:     2,
		Attempts:    3,
		Deadline:    deadline,
		Step:        4,
		Steps:       6,
		Stage:       "upload",
		Cause:       "SFTP unavailable; using exec",
		Transport:   "exec",
		Path:        "/remote/helper",
		Transferred: 128,
		Total:       512,
		Percent:     25,
	}

	event := productionHelperProgressEvent(scope, progress)
	if event.SessionID != scope.SessionID || event.AssetID != scope.AssetID {
		t.Fatalf("event scope = %q/%q, want %q/%q", event.SessionID, event.AssetID, scope.SessionID, scope.AssetID)
	}
	if event.OperationID != progress.OperationID || event.Attempt != 2 || event.Attempts != 3 {
		t.Fatalf("operation identity = %q attempt %d/%d", event.OperationID, event.Attempt, event.Attempts)
	}
	if event.Deadline != deadline.UnixMilli() {
		t.Fatalf("deadline = %d, want %d", event.Deadline, deadline.UnixMilli())
	}
	if event.Stage != "upload" || event.Cause != progress.Cause || event.Path != progress.Path || event.Transport != "exec" {
		t.Fatalf("operation evidence = %+v", event)
	}
	if event.Step != 4 || event.Steps != 6 || event.Transferred != 128 || event.Total != 512 || event.Percent != 25 {
		t.Fatalf("legacy progress fields regressed: %+v", event)
	}
}

func TestProductionHelperOperationCancelIPC(t *testing.T) {
	started := make(chan context.Context, 1)
	resolver := session.DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		session.ReportHelperOperation(ctx, "operation-ipc-cancel")
		started <- ctx
		<-ctx.Done()
		return nil, context.Cause(ctx)
	})
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)
	attachedResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "helper-cancel-channel", "client-a")
	tabID := requireAttachedTabID(t, attachedResponse)
	var deploymentCtx context.Context
	select {
	case deploymentCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("background helper deployment did not start")
	}

	response := dispatchDurableTest(t, production, "terminal_helper_operation", `{"sessionId":"`+connected.ID+`","operationId":"operation-ipc-cancel","action":"cancel"}`, "", "client-a")
	var result helperOperationDTO
	requireStoreTestResponse(t, response, &result)
	if !result.Accepted || result.Action != "cancel" || result.OperationID != "operation-ipc-cancel" || result.Compatibility != "unknown" {
		t.Fatalf("cancel result = %+v", result)
	}
	if result.Local.Known || result.Running.Known {
		t.Fatalf("cancel fabricated version evidence: %+v", result)
	}
	select {
	case <-deploymentCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("IPC did not cancel the real deployment context")
	}
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil || tab.Info().Durable {
		t.Fatalf("ordinary tab after IPC cancellation = %+v, %v", tab, err)
	}
	transport, err := production.Services.Sessions.Transport(t.Context(), connected.ID)
	if err != nil || !transport.IsAlive() {
		t.Fatalf("IPC cancellation disconnected SSH: %v, %v", transport, err)
	}
}

func TestHelperDTOFromDaemonReportsEvidenceAndRealUpdateOutcome(t *testing.T) {
	protocol := supervisor.ProtocolVersion
	local := supervisor.RuntimeMetadata{Version: "v1.4.0", Commit: "local-commit", Protocol: protocol, SHA256: "aaaa"}
	running := supervisor.RuntimeMetadata{Version: "v1.3.9", Commit: "running-commit", Protocol: protocol, SHA256: "bbbb"}
	daemon := &sshdaemon.Daemon{
		RunningMetadata: running,
		MetadataKnown:   true,
		Update:          sshdaemon.HelperUpdateStatus{Local: local, Running: running, Updated: true},
	}
	result := helperDTOFromDaemon("session-a", "update", daemon)
	if result.Local.Release != "v1.4.0" || result.Local.Commit != "local-commit" || result.Local.Protocol == nil || *result.Local.Protocol != protocol || result.Local.SHA256 != "aaaa" {
		t.Fatalf("local metadata = %+v", result.Local)
	}
	if result.Running.Release != "v1.3.9" || result.Running.Commit != "running-commit" || result.Running.Protocol == nil || *result.Running.Protocol != protocol || result.Running.SHA256 != "bbbb" {
		t.Fatalf("running metadata = %+v", result.Running)
	}
	if result.Compatibility != "compatible" {
		t.Fatalf("release difference changed protocol compatibility: %q", result.Compatibility)
	}
	if result.Update == nil || result.Update.Status != "updated" {
		t.Fatalf("update outcome = %+v", result.Update)
	}

	daemon.MetadataKnown = false
	daemon.RunningMetadata = supervisor.RuntimeMetadata{}
	daemon.Update = sshdaemon.HelperUpdateStatus{Deferred: true, Reason: "active PTY"}
	result = helperDTOFromDaemon("session-a", "update", daemon)
	if result.Running.Known || result.Compatibility != "unknown" {
		t.Fatalf("unknown running evidence was fabricated: %+v, %q", result.Running, result.Compatibility)
	}
	if result.Update == nil || result.Update.Status != "deferred" || result.Update.Detail == "" {
		t.Fatalf("deferred outcome = %+v", result.Update)
	}
}

func writeOfflineHelperManifest(t *testing.T, path string, protocol int, verified bool) {
	t.Helper()
	initial, err := sshdaemon.InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cgo := "0"
	if initial.CGO {
		cgo = "1"
	}
	manifest := map[string]any{
		"schema_version":           1,
		"kind":                     "go-binary",
		"version":                  initial.Version,
		"commit":                   initial.Commit,
		"goarm":                    initial.GOARM,
		"sha256":                   initial.Digest,
		"binary_metadata_verified": verified,
		"platform":                 map[string]any{"os": initial.GOOS, "arch": helperManifestArchitecture(initial), "cgo": cgo, "goarm": initial.GOARM},
		"artifact":                 map[string]any{"size_bytes": info.Size(), "sha256": initial.Digest},
	}
	if verified {
		manifest["protocol"] = protocol
		manifest["metadata_host"] = map[string]any{"os": initial.GOOS, "arch": helperManifestArchitecture(initial)}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".manifest.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeOfflineHelperFixture(t *testing.T, protocol int, verified bool) (string, sshdaemon.UploadSource) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "offline-helper")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOfflineHelperManifest(t, path, protocol, verified)
	source, err := sshdaemon.InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, source
}

func TestHelperOfflineTrustRequiresActualSHAAndVerifiedProtocol(t *testing.T) {
	for _, test := range []struct {
		name          string
		protocol      int
		verified      bool
		compatibility string
	}{
		{name: "compatible", protocol: supervisor.ProtocolVersion, verified: true, compatibility: "compatible"},
		{name: "incompatible", protocol: supervisor.ProtocolVersion + 1, verified: true, compatibility: "incompatible"},
		{name: "unknown", protocol: supervisor.ProtocolVersion, verified: false, compatibility: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			production, _ := newRemoteRecoveryHarness(t, &recordingDurableResolver{err: errors.New("no SSH resolution expected")})
			connected := connectRemoteSSHAsset(t, production)
			connectedSession, err := production.Services.Sessions.Session(connected.ID)
			if err != nil {
				t.Fatal(err)
			}
			assetID := connectedSession.Asset().ID
			service := &productionHelperService{sessions: production.Services.Sessions}
			path, source := writeOfflineHelperFixture(t, test.protocol, test.verified)

			inspected, err := service.inspectOffline(t.Context(), connected.ID, path, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if inspected.Offline == nil || inspected.Offline.SHA256 != source.Digest || inspected.Offline.Compatibility != test.compatibility || inspected.Offline.Trusted {
				t.Fatalf("offline inspection = %+v", inspected.Offline)
			}
			resolver := &sshdaemon.Resolver{}
			service.configureResolver(resolver, assetID)
			if resolver.HelperExecutable != "" {
				t.Fatal("inspection trusted the raw helper without confirmation")
			}
			if _, err := service.inspectOffline(t.Context(), connected.ID, path, "incorrect-sha", true); err == nil {
				t.Fatal("incorrect SHA was trusted")
			}

			confirmed, err := service.inspectOffline(t.Context(), connected.ID, path, source.Digest, true)
			if test.compatibility == "compatible" {
				if err != nil {
					t.Fatalf("compatible offline helper was rejected: %v", err)
				}
				if !confirmed.Accepted || confirmed.Offline == nil || !confirmed.Offline.Trusted {
					t.Fatalf("offline confirmation = %+v", confirmed)
				}
				service.configureResolver(resolver, assetID)
				if resolver.HelperExecutable != path || resolver.HelperSHA256 != source.Digest {
					t.Fatalf("resolver helper/digest = %q/%q, want %q/%q", resolver.HelperExecutable, resolver.HelperSHA256, path, source.Digest)
				}
			} else {
				if err == nil {
					t.Fatal("unknown or incompatible protocol was trusted")
				}
				service.configureResolver(resolver, assetID)
				if resolver.HelperExecutable != "" {
					t.Fatal("rejected offline helper changed resolver configuration")
				}
			}
		})
	}
}

func TestOfflineTrustRejectsConfirmedPathReplacement(t *testing.T) {
	production, _ := newRemoteRecoveryHarness(t, &recordingDurableResolver{err: errors.New("no SSH resolution expected")})
	connected := connectRemoteSSHAsset(t, production)
	connectedSession, err := production.Services.Sessions.Session(connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	assetID := connectedSession.Asset().ID
	service := &productionHelperService{sessions: production.Services.Sessions}
	path, source := writeOfflineHelperFixture(t, supervisor.ProtocolVersion, true)
	if _, err := service.inspectOffline(t.Context(), connected.ID, path, source.Digest, true); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, 0)
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(path + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["sha256"] = digest
	artifact := manifest["artifact"].(map[string]any)
	artifact["size_bytes"] = len(content)
	artifact["sha256"] = digest
	manifestRaw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".manifest.json", manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sshdaemon.InspectExecutable(path); err != nil {
		t.Fatal(err)
	}
	resolver := &sshdaemon.Resolver{HelperExecutable: "/packaged/helper", HelperSHA256: "packaged-sha"}
	if err := service.configureResolver(resolver, assetID); err == nil {
		t.Fatal("confirmed path replacement was trusted")
	}
	if resolver.HelperExecutable != "/packaged/helper" || resolver.HelperSHA256 != "packaged-sha" {
		t.Fatalf("resolver changed after rejected replacement: %q/%q", resolver.HelperExecutable, resolver.HelperSHA256)
	}
}

func TestOfflineTrustRejectsReplacementDuringDeployment(t *testing.T) {
	oldPath, newPath := buildHelperUpdateBinaries(t)
	writeOfflineHelperManifest(t, oldPath, supervisor.ProtocolVersion, true)
	writeOfflineHelperManifest(t, newPath, supervisor.ProtocolVersion, true)
	oldSource, err := sshdaemon.InspectExecutable(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	newSource, err := sshdaemon.InspectExecutable(newPath)
	if err != nil {
		t.Fatal(err)
	}
	service, manager, sessionID := newHelperUpdateService(t, oldPath)
	if _, err := service.inspectOffline(t.Context(), sessionID, oldPath, oldSource.Digest, true); err != nil {
		t.Fatal(err)
	}
	connected, err := manager.Session(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := manager.TransportFor(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := transport.(*transportssh.Client)
	if !ok {
		t.Fatalf("offline deployment transport = %T", transport)
	}
	resolver := &sshdaemon.Resolver{
		HelperExecutable: "/packaged/helper",
		StartTimeout:     5 * time.Second,
		EnsureTimeout:    30 * time.Second,
	}
	if err := service.configureResolver(resolver, connected.Asset().ID); err != nil {
		t.Fatal(err)
	}
	if resolver.HelperExecutable != oldPath {
		t.Fatalf("configured helper = %q, want confirmed path %q", resolver.HelperExecutable, oldPath)
	}
	replaced := false
	uploaded := false
	probed := false
	discovers := 0
	resolver.Progress = func(_ context.Context, progress sshdaemon.HelperProgress) {
		if progress.Stage == "discover" {
			discovers++
			if !replaced {
				replaced = true
				content, err := os.ReadFile(newPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(oldPath, content, 0o755); err != nil {
					t.Fatal(err)
				}
				manifest, err := os.ReadFile(newPath + ".manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(oldPath+".manifest.json", manifest, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if replaced && progress.Stage == "upload" {
			uploaded = true
		}
		if replaced && progress.Stage == "probe" && strings.Contains(progress.Path, newSource.Digest) {
			probed = true
		}
	}
	daemon, err := resolver.EnsureDaemon(t.Context(), client)
	if !replaced {
		t.Fatal("helper was not replaced during deployment")
	}
	if uploaded || probed || daemon != nil {
		t.Fatalf("unconfirmed replacement was used: upload=%v probe=%v daemon=%+v err=%v", uploaded, probed, daemon, err)
	}
	if !errors.Is(err, sshdaemon.ErrDigestMismatch) {
		t.Fatalf("deployment replacement error = %v, want ErrDigestMismatch", err)
	}
	if discovers != 1 {
		t.Fatalf("digest failure used %d deployment attempts, want permanent failure", discovers)
	}
}

type helperUpdateExecServer struct {
	listener net.Listener
	hostKey  gossh.PublicKey
	config   *gossh.ServerConfig
	home     string
	test     *testing.T
	pty      atomic.Int32
	sftp     atomic.Int32
}

func newHelperUpdateExecServer(t *testing.T) *helperUpdateExecServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
		if metadata.User() == "test" && string(password) == "secret" {
			return nil, nil
		}
		return nil, errors.New("invalid test credentials")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("/tmp", "nxh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	server := &helperUpdateExecServer{listener: listener, hostKey: signer.PublicKey(), config: config, home: home, test: t}
	for _, directory := range []string{filepath.Join(server.home, "tmp"), filepath.Join(server.home, "runtime")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	go server.accept()
	t.Cleanup(func() {
		_ = listener.Close()
		if t.Failed() {
			logPath := filepath.Join(server.home, "data", "nexterm", "daemon", "helper.log")
			if raw, err := os.ReadFile(logPath); err == nil {
				t.Logf("remote helper log:\n%s", raw)
			} else {
				t.Logf("read remote helper log: %v", err)
			}
		}
		raw, err := os.ReadFile(filepath.Join(server.home, "data", "nexterm", "daemon", "helper.pid"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return
		}
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Kill()
		}
	})
	return server
}

func (s *helperUpdateExecServer) accept() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConnection(connection)
	}
}

func (s *helperUpdateExecServer) handleConnection(connection net.Conn) {
	serverConnection, channels, requests, err := gossh.NewServerConn(connection, s.config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer func() {
		s.test.Logf("test SSH connection ended: %v", serverConnection.Wait())
		_ = serverConnection.Close()
	}()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err == nil {
			go s.handleSession(channel, channelRequests)
		}
	}
}

func (s *helperUpdateExecServer) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req":
			s.pty.Add(1)
			_ = request.Reply(false, nil)
		case "subsystem":
			s.sftp.Add(1)
			_ = request.Reply(false, nil)
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			s.execute(channel, payload.Command)
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func (s *helperUpdateExecServer) execute(channel gossh.Channel, command string) {
	process := exec.Command("/bin/sh", "-c", command)
	process.Env = append(os.Environ(),
		"HOME="+s.home,
		"XDG_DATA_HOME="+filepath.Join(s.home, "data"),
		"XDG_RUNTIME_DIR="+filepath.Join(s.home, "runtime"),
		"TMPDIR="+filepath.Join(s.home, "tmp"),
	)
	var stdinWriter *os.File
	if strings.HasPrefix(command, "umask 077 && cat > ") || strings.Contains(command, " --bridge ") {
		stdinReader, writer, err := os.Pipe()
		if err != nil {
			_, _ = channel.Stderr().Write([]byte(err.Error()))
			_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{Status: 127}))
			return
		}
		process.Stdin = stdinReader
		stdinWriter = writer
		defer stdinReader.Close()
		defer writer.Close()
	} else {
		process.Stdin = strings.NewReader("")
	}
	process.Stdout = channel
	process.Stderr = channel.Stderr()
	err := process.Start()
	if err == nil {
		if stdinWriter != nil {
			go func() {
				_, _ = io.Copy(stdinWriter, channel)
				_ = stdinWriter.Close()
			}()
		}
		err = process.Wait()
	}
	status := uint32(0)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = uint32(exitErr.ExitCode())
		} else {
			_, _ = channel.Stderr().Write([]byte(err.Error()))
			status = 127
		}
	}
	_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{Status: status}))
}

func (s *helperUpdateExecServer) connect(t *testing.T) *transportssh.Client {
	t.Helper()
	host, portText, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := transportssh.Connect(context.Background(), transportssh.Config{
		Host: host, Port: port, User: "test",
		Auth:              transportssh.AuthConfig{Method: transportssh.AuthPassword, Password: "secret"},
		HostKeys:          transportssh.NewMemoryHostKeyStore(),
		AutoAcceptUnknown: true,
		ConnectTimeout:    3 * time.Second,
		KeepAliveInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func helperUpdateBuildRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func buildHelperUpdateBinaries(t *testing.T) (string, string) {
	t.Helper()
	root := helperUpdateBuildRoot(t)
	directory := t.TempDir()
	oldPath := filepath.Join(directory, "helper-old")
	command := exec.Command("go", "build", "-o", oldPath, "./cmd/nexterm-helper")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build old helper: %v\n%s", err, output)
	}
	content, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(directory, "helper-new")
	if err := os.WriteFile(newPath, append(content, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSource, err := sshdaemon.InspectExecutable(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	newSource, err := sshdaemon.InspectExecutable(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(oldSource.Digest, newSource.Digest) {
		t.Fatal("update fixture did not change the helper digest")
	}
	return oldPath, newPath
}

func buildFailingHelper(t *testing.T) string {
	t.Helper()
	root := helperUpdateBuildRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(filepath.Join(root, ".tmp"), "helper-r1-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	source := `package main

import (
	"fmt"
	"os"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	if os.Args[1] == supervisor.HelperCommand && len(os.Args) > 2 && os.Args[2] == "--probe" {
		fmt.Println(supervisor.ProtocolVersion)
		return
	}
	os.Exit(1)
}
`
	path := filepath.Join(directory, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "helper-failing")
	command := exec.Command("go", "build", "-o", binary, path)
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build failing helper: %v\n%s", err, output)
	}
	return binary
}

func newHelperUpdateService(t *testing.T, helperPath string) (*productionHelperService, *session.Manager, string) {
	t.Helper()
	server := newHelperUpdateExecServer(t)
	client := server.connect(t)
	resolver := &sshdaemon.Resolver{
		HelperExecutable: helperPath,
		StartTimeout:     20 * time.Second,
		EnsureTimeout:    120 * time.Second,
		Progress: func(_ context.Context, progress sshdaemon.HelperProgress) {
			t.Logf("helper progress: %+v", progress)
		},
	}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return client, nil
		}),
		DurableResolver: resolver,
		OpenAccess:      true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "helper-update-host", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return &productionHelperService{ssh: resolver, sessions: manager}, manager, connected.ID
}

func dispatchHelperOperation(t *testing.T, service *productionHelperService, sessionID, action string) ipc.Response {
	t.Helper()
	terminalService := &terminalCommandService{sessions: service.sessions, helpers: service, desktop: true}
	dispatcher := ipc.NewDispatcher()
	if err := terminalService.registerSession(dispatcher); err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"sessionId":"` + sessionID + `","action":"` + action + `"}`)
	return dispatcher.Dispatch(t.Context(), ipc.Request{Command: "terminal_helper_operation", Args: args, ClientID: "client-a"}, ipc.Environment{ClientID: "client-a"})
}

func TestProductionHelperUpdatePreservesInterruptedOutcome(t *testing.T) {
	oldPath, newPath := buildHelperUpdateBinaries(t)
	service, manager, sessionID := newHelperUpdateService(t, oldPath)
	transport, err := manager.TransportFor(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := transport.(*transportssh.Client)
	if !ok {
		t.Fatalf("helper update transport = %T", transport)
	}
	daemon, err := service.ssh.EnsureDaemon(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := daemon.Provider().Create(t.Context(), base.DurableCreateOptions{
		ID: "0123456789abcdef0123456789", Command: []string{"sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = attachment.Kill(context.Background())
		_ = attachment.Close()
	})
	service.ssh.HelperExecutable = newPath
	response := dispatchHelperOperation(t, service, sessionID, "update_interrupt")
	var result helperOperationDTO
	requireStoreTestResponse(t, response, &result)
	if result.Update == nil || !result.Update.Updated || !result.Update.Interrupted || result.Update.RolledBack {
		t.Fatalf("update outcome lost interrupted evidence: %+v", result)
	}
	if result.Update.Status != "interrupted" {
		t.Fatalf("updated+interrupted collapsed to %q", result.Update.Status)
	}
}

func TestProductionHelperRollbackReturnsStructuredResult(t *testing.T) {
	oldPath, _ := buildHelperUpdateBinaries(t)
	failingPath := buildFailingHelper(t)
	service, _, sessionID := newHelperUpdateService(t, oldPath)
	if _, err := service.ensure(t.Context(), sessionID, "update"); err != nil {
		t.Fatal(err)
	}
	service.ssh.HelperExecutable = failingPath
	response := dispatchHelperOperation(t, service, sessionID, "update")
	if response.OK || response.Error == nil {
		t.Fatalf("rollback unexpectedly succeeded: %+v", response)
	}
	detail, ok := response.Error.Detail.(helperOperationDTO)
	if !ok {
		t.Fatalf("rollback error = %+v, detail = %T; want helperOperationDTO", response.Error, response.Error.Detail)
	}
	if detail.Update == nil || !detail.Update.RolledBack || detail.Update.Updated {
		t.Fatalf("rollback outcome = %+v", detail.Update)
	}
	if detail.Update.Status != "rolled_back" {
		t.Fatalf("rollback status = %q", detail.Update.Status)
	}
}
