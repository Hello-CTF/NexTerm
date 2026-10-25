//go:build unix

package ssh

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

// fastFixture is a target that already has the helper installed and its daemon
// running, so every ensure takes the "nothing to install" route.
type fastFixture struct {
	host     *fakeHost
	resolver *Resolver
	client   *fakeDaemonClient
	digest   string
	binary   string
	stateDir string

	mu        sync.Mutex
	metadata  func() (supervisor.RuntimeMetadata, bool, error)
	identity  string
	bridgeUse int
}

func newFastFixture(t *testing.T) *fastFixture {
	t.Helper()
	f := &fastFixture{digest: strings.Repeat("ab", 32), identity: "alice@ssh-ed25519 SHA256:host-a"}
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: t.TempDir(), Identity: "501"}
	paths, err := target.Paths()
	if err != nil {
		t.Fatal(err)
	}
	helper, err := paths.Helper(f.digest)
	if err != nil {
		t.Fatal(err)
	}
	f.binary, f.stateDir = helper, paths.StateDir
	f.host = &fakeHost{
		discovery:    target.DataRoot + "\nLinux\namd64\n501\n",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		exists:       map[string]bool{paths.StateDir: true, helper: true},
		digests:      map[string]string{helper: f.digest},
	}
	f.metadata = func() (supervisor.RuntimeMetadata, bool, error) {
		return supervisor.RuntimeMetadata{Version: "v1", Commit: "c", Protocol: supervisor.ProtocolVersion, SHA256: f.digest}, true, nil
	}
	f.client = &fakeDaemonClient{metadata: func(context.Context) (supervisor.RuntimeMetadata, bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bridgeUse++
		return f.metadata()
	}}
	f.resolver = &Resolver{
		HelperExecutable: "/offline/nexterm-helper",
		StartTimeout:     50 * time.Millisecond,
		EnsureTimeout:    5 * time.Second,
		RetryBackoff:     -1,
		hostFor:          func(context.Context, *transportssh.Client) (Host, error) { return f.host, nil },
		clientFor:        func(*Daemon) remoteDaemonClient { return f.client },
		inspect: func(string) (UploadSource, error) {
			return UploadSource{Path: "/offline/nexterm-helper", GOOS: "linux", GOARCH: "amd64", Digest: f.digest, Version: "v1"}, nil
		},
		hostIdentity: func(Host, *transportssh.Client) string { return f.identity },
	}
	f.resolver.EnableDeploymentMemory()
	return f
}

func (f *fastFixture) ensure(t *testing.T) (*Daemon, int) {
	t.Helper()
	f.host.mu.Lock()
	before := len(f.host.commands)
	f.host.mu.Unlock()
	daemon, err := f.resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f.host.mu.Lock()
	defer f.host.mu.Unlock()
	return daemon, len(f.host.commands) - before
}

func (f *fastFixture) discoveries() int {
	f.host.mu.Lock()
	defer f.host.mu.Unlock()
	count := 0
	for _, command := range f.host.commands {
		if strings.Contains(command, "uname -s") {
			count++
		}
	}
	return count
}

func TestReconnectUsesFastPathWithFewRemoteCommands(t *testing.T) {
	f := newFastFixture(t)
	first, fullCommands := f.ensure(t)
	if first.Binary != f.binary || fullCommands < 4 {
		t.Fatalf("first ensure binary=%q commands=%d; want the full discovery flow", first.Binary, fullCommands)
	}
	second, commands := f.ensure(t)
	if second.Binary != f.binary || second.StateDir != f.stateDir || !second.MetadataKnown {
		t.Fatalf("fast daemon = %+v", second)
	}
	// The helper digest check is a filesystem call on the fake host and the bridge is not a Run.
	if commands > 3 || commands >= fullCommands {
		t.Fatalf("fast reconnect ran %d remote commands, want at most 3 (full flow ran %d)", commands, fullCommands)
	}
	if f.discoveries() != 1 {
		t.Fatalf("fast reconnect repeated target discovery (%d total)", f.discoveries())
	}
	if second.Update.Local.SHA256 != f.digest || second.Update.Running.SHA256 != f.digest {
		t.Fatalf("update evidence not populated: %+v", second.Update)
	}
}

func TestFastPathFallsBackToFullFlowWhenVerificationFails(t *testing.T) {
	cases := map[string]func(f *fastFixture){
		"daemon unreachable once": func(f *fastFixture) {
			calls := 0
			f.metadata = func() (supervisor.RuntimeMetadata, bool, error) {
				calls++
				if calls == 1 {
					return supervisor.RuntimeMetadata{}, false, errors.New("bridge exited")
				}
				return supervisor.RuntimeMetadata{Protocol: supervisor.ProtocolVersion, SHA256: f.digest, Version: "v1"}, true, nil
			}
		},
		"metadata unproven": func(f *fastFixture) {
			calls := 0
			f.metadata = func() (supervisor.RuntimeMetadata, bool, error) {
				calls++
				return supervisor.RuntimeMetadata{Protocol: supervisor.ProtocolVersion, SHA256: f.digest, Version: "v1"}, calls > 1, nil
			}
		},
		"protocol changed once": func(f *fastFixture) {
			calls := 0
			f.metadata = func() (supervisor.RuntimeMetadata, bool, error) {
				calls++
				protocol := supervisor.ProtocolVersion
				if calls == 1 {
					protocol++
				}
				return supervisor.RuntimeMetadata{Protocol: protocol, SHA256: f.digest, Version: "v1"}, true, nil
			}
		},
		"daemon runs a different helper once": func(f *fastFixture) {
			calls := 0
			f.metadata = func() (supervisor.RuntimeMetadata, bool, error) {
				calls++
				digest := f.digest
				if calls == 1 {
					digest = strings.Repeat("cd", 32)
				}
				return supervisor.RuntimeMetadata{Protocol: supervisor.ProtocolVersion, SHA256: digest, Version: "v1"}, true, nil
			}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFastFixture(t)
			f.ensure(t)
			arrange(f)
			// The injected failure hits the fast path's first metadata read; the
			// full flow must then succeed on its own evidence.
			daemon, _ := f.ensure(t)
			if daemon.Binary != f.binary {
				t.Fatalf("fallback daemon binary = %q", daemon.Binary)
			}
			if f.discoveries() != 2 {
				t.Fatalf("stale fast path did not fall back to full discovery (%d discoveries)", f.discoveries())
			}
		})
	}
}

func TestFastPathRejectsHelperFileWithWrongDigest(t *testing.T) {
	f := newFastFixture(t)
	f.ensure(t)
	f.host.mu.Lock()
	f.host.digests[f.binary] = strings.Repeat("ee", 32)
	f.host.mu.Unlock()
	if _, err := f.resolver.EnsureDaemon(context.Background(), nil); err == nil {
		// The full flow may repair the helper by reinstalling; either way the
		// tampered file must not have been accepted through the cache.
		t.Log("full flow recovered after fast-path rejection")
	}
	if f.discoveries() < 2 {
		t.Fatalf("a helper file with a different digest was trusted from the cache (%d discoveries)", f.discoveries())
	}
}

func TestFastPathDoesNotUseCacheWhenLocalHelperChanged(t *testing.T) {
	f := newFastFixture(t)
	f.ensure(t)
	newDigest := strings.Repeat("cd", 32)
	f.resolver.inspect = func(string) (UploadSource, error) {
		return UploadSource{Path: "/offline/nexterm-helper", GOOS: "linux", GOARCH: "amd64", Digest: newDigest, Version: "v2"}, nil
	}
	f.resolver.DisableAutoInstall = true
	f.host.pathBinary = f.binary
	daemon, err := f.resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.discoveries() != 2 {
		t.Fatal("a running daemon with a different digest than the local helper was reused through the cache")
	}
	if daemon.Update.Local.SHA256 != newDigest || daemon.Update.Running.SHA256 != f.digest {
		t.Fatalf("full flow evidence = %+v", daemon.Update)
	}
}

func TestFastPathNeedsLocalHelperEvidence(t *testing.T) {
	f := newFastFixture(t)
	f.ensure(t)
	f.resolver.inspect = func(string) (UploadSource, error) { return UploadSource{}, errors.New("helper unreadable") }
	if _, err := f.resolver.EnsureDaemon(context.Background(), nil); err != nil {
		t.Logf("full flow result without local evidence: %v", err)
	}
	if f.discoveries() < 2 {
		t.Fatal("cached deployment was used without any local helper evidence")
	}
}

func TestDeploymentCacheIsKeyedByHostIdentity(t *testing.T) {
	a := newFastFixture(t)
	a.ensure(t)
	b := newFastFixture(t)
	// Same resolver state, different machine behind the connection.
	b.resolver = a.resolver
	b.resolver.hostFor = func(context.Context, *transportssh.Client) (Host, error) { return b.host, nil }
	b.resolver.clientFor = func(*Daemon) remoteDaemonClient { return b.client }
	b.resolver.inspect = func(string) (UploadSource, error) {
		return UploadSource{Path: "/offline/nexterm-helper", GOOS: "linux", GOARCH: "amd64", Digest: b.digest, Version: "v1"}, nil
	}
	current := "bob@ssh-ed25519 SHA256:host-b"
	b.resolver.hostIdentity = func(Host, *transportssh.Client) string { return current }
	if _, err := b.resolver.EnsureDaemon(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if b.discoveries() != 1 {
		t.Fatalf("another host's cache entry was reused (%d discoveries)", b.discoveries())
	}
	// Now host b is known too and reuses its own entry, host a keeps its own.
	before := len(b.host.commands)
	if _, err := b.resolver.EnsureDaemon(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if b.discoveries() != 1 || len(b.host.commands)-before > 3 {
		t.Fatalf("host b did not get its own fast path: discoveries=%d commands=%d", b.discoveries(), len(b.host.commands)-before)
	}
	if entry, ok := a.resolver.deployments.get("alice@ssh-ed25519 SHA256:host-a"); !ok || entry.binary != a.binary {
		t.Fatalf("host a entry lost: %+v %v", entry, ok)
	}
}

func TestDeploymentCacheIsNotUsedWithoutHostIdentity(t *testing.T) {
	f := newFastFixture(t)
	f.identity = ""
	f.ensure(t)
	f.ensure(t)
	if f.discoveries() != 2 {
		t.Fatalf("an unidentified host was cached (%d discoveries)", f.discoveries())
	}
}

func TestTemporaryRootDeploymentsAreNotRemembered(t *testing.T) {
	r := &Resolver{}
	r.EnableDeploymentMemory()
	r.rememberDeployment("k", &Daemon{Binary: "/tmp/x", Temporary: true, MetadataKnown: true})
	r.rememberDeployment("k2", &Daemon{Binary: "/b", MetadataKnown: false})
	if _, ok := r.deployments.get("k"); ok {
		t.Fatal("temporary-root daemon was remembered")
	}
	if _, ok := r.deployments.get("k2"); ok {
		t.Fatal("a daemon without proven metadata was remembered")
	}
}

func countCommands(host *fakeHost, fragment string) int {
	host.mu.Lock()
	defer host.mu.Unlock()
	count := 0
	for _, command := range host.commands {
		if strings.Contains(command, fragment) {
			count++
		}
	}
	return count
}

func TestFullFlowSkipsFallbackDiscoveryAndRepeatedProbesWhenPrimaryIsUsable(t *testing.T) {
	f := newFastFixture(t)
	f.identity = "" // keep every ensure on the full flow
	f.ensure(t)
	if got := countCommands(f.host, "XDG_RUNTIME_DIR"); got != 0 {
		t.Fatalf("fallback roots were discovered although the primary state directory exists (%d times)", got)
	}
	if got := countCommands(f.host, "command -v nexterm-helper"); got != 1 {
		t.Fatalf("PATH lookup ran %d times, want once per cycle", got)
	}
	if got := countCommands(f.host, "--probe"); got != 1 {
		t.Fatalf("helper probe ran %d times, want once per cycle", got)
	}
}

func TestColdStartBatchesDirectoryChecksAndLookupsAcrossLocations(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	target := dataRoot + "/nexterm/bin/nexterm-helper-" + digest
	runtimeDir := t.TempDir()
	host := &fakeHost{
		discovery:         testDiscovery(t, dataRoot),
		fallbackDiscovery: runtimeDir + "\n\n",
		probeVersion:      strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:        fixture.socketPath,
		digests:           map[string]string{target: digest},
		uploadDigest:      digest,
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
	if _, err := resolver.EnsureDaemon(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := countCommands(host, "command -v nexterm-helper"); got != 1 {
		t.Fatalf("PATH lookup ran %d times across locations, want once", got)
	}
	if got := countCommands(host, "--probe"); got > 2 {
		t.Fatalf("helper probed %d times, want at most the staged and final check", got)
	}
	// Three NexTerm directories per validated location and the ancestor walk of
	// each created directory are single commands, not one per path.
	if got := countCommands(host, "nexterm-directory-kind"); got > 12 {
		t.Fatalf("directory kind probes = %d; checks are no longer batched", got)
	}
}

func TestEnsureRemoteDirectoryClassifiesWholeAncestorChainInOneCommand(t *testing.T) {
	root := t.TempDir()
	host := &fakeHost{}
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: root, Identity: "1"}
	deep := root + "/a/b/c/d"
	if err := ensureRemoteDirectory(context.Background(), host, target, deep, true); err != nil {
		t.Fatal(err)
	}
	// Walk-up is one command; each of the four created levels is verified once.
	if got := countCommands(host, "nexterm-directory-kind"); got != 5 {
		t.Fatalf("directory kind commands = %d, want 1 walk + 4 verifications", got)
	}
	want := []string{root + "/a", root + "/a/b", root + "/a/b/c", deep}
	host.mu.Lock()
	defer host.mu.Unlock()
	if strings.Join(host.mkdirs, ",") != strings.Join(want, ",") {
		t.Fatalf("mkdirs = %v, want parent-first %v", host.mkdirs, want)
	}
}

func TestBatchedDirectoryChecksStillRefuseUnsafeKinds(t *testing.T) {
	root := t.TempDir()
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: root, Identity: "1"}
	for _, kind := range []string{"symlink", "other"} {
		host := &fakeHost{directoryKinds: map[string]string{root + "/x": kind}}
		err := ensureRemoteDirectory(context.Background(), host, target, root+"/x", true)
		if err == nil {
			t.Fatalf("%s directory was accepted", kind)
		}
	}
	paths, _ := target.Paths()
	host := &fakeHost{directoryKinds: map[string]string{paths.BinDir: "symlink"}}
	err := validateNexTermPaths(context.Background(), host, deploymentLocation{target: target, paths: paths, temporary: true})
	if !errors.Is(err, errUnsafeTemporaryRoot) {
		t.Fatalf("symlinked private path error = %v", err)
	}
	if got := countCommands(host, "nexterm-directory-kind"); got != 1 {
		t.Fatalf("validating three private paths took %d commands", got)
	}
}

// Resolvers are copied per session; the copies must share one memory, and a resolver without memory must simply
// not use the fast path instead of misbehaving.
func TestDeploymentMemoryIsSharedByResolverCopiesAndOptional(t *testing.T) {
	shared := &Resolver{}
	shared.EnableDeploymentMemory()
	first := *shared
	second := *shared
	first.rememberDeployment("host", &Daemon{Binary: "/b", MetadataKnown: true})
	if _, ok := second.deployments.get("host"); !ok {
		t.Fatal("a copy of the resolver did not see the deployment remembered through another copy")
	}

	var disabled Resolver
	disabled.rememberDeployment("host", &Daemon{Binary: "/b", MetadataKnown: true})
	if _, ok := disabled.deployments.get("host"); ok {
		t.Fatal("a resolver without deployment memory remembered something")
	}
}
