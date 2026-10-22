//go:build unix

package ssh

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

type installLockHost struct {
	Host
	lock            string
	mkdirErr        error
	releaseOnExists bool
	mkdirCalls      int
	existsCalls     int
}

func (h *installLockHost) Mkdir(ctx context.Context, path string) error {
	if path != h.lock {
		return h.Host.Mkdir(ctx, path)
	}
	h.mkdirCalls++
	if h.mkdirErr != nil && h.mkdirCalls == 1 {
		return h.mkdirErr
	}
	if h.mkdirCalls > 1 {
		return h.Host.Mkdir(ctx, path)
	}
	if err := h.Host.Mkdir(ctx, path); err != nil {
		return err
	}
	return fs.ErrExist
}

func (h *installLockHost) Exists(ctx context.Context, path string) (bool, error) {
	if path == h.lock {
		h.existsCalls++
		if h.releaseOnExists && h.existsCalls == 1 {
			if err := h.Host.Delete(ctx, path, true); err != nil {
				return false, err
			}
		}
	}
	return h.Host.Exists(ctx, path)
}

func stageInstallLock(t *testing.T, host Host, binDir string, upload UploadSource) (string, error) {
	t.Helper()
	target := Target{GOOS: "linux", GOARCH: upload.GOARCH, GOARM: upload.GOARM, DataRoot: filepath.Dir(filepath.Dir(binDir)), Identity: "0"}
	return StageUpload(context.Background(), host, target, upload, false, nil)
}

func TestInstallRetriesWhenContendedLockDisappears(t *testing.T) {
	digest := strings.Repeat("78", 32)
	shared := newSharedTarget("")
	binDir := filepath.Join(t.TempDir(), "nexterm", "bin")
	lock := binDir + "/.nexterm-" + digest + ".install"
	host := &installLockHost{
		Host:            &fakeHost{shared: shared, uploadDigest: digest},
		lock:            lock,
		releaseOnExists: true,
	}
	upload := fakeUploadSource(digest)
	target, err := stageInstallLock(t, host, binDir, upload)
	if err != nil {
		t.Fatal(err)
	}
	if want := binDir + "/nexterm-helper-" + digest; target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if host.mkdirCalls != 2 || host.existsCalls != 1 {
		t.Fatalf("lock calls = %d mkdir/%d exists, want 2/1", host.mkdirCalls, host.existsCalls)
	}
	if shared.exists(lock) {
		t.Fatal("installation lock remained after cleanup")
	}
}

func TestInstallReturnsNonExistLockErrorWithoutRetry(t *testing.T) {
	digest := strings.Repeat("90", 32)
	shared := newSharedTarget("")
	binDir := filepath.Join(t.TempDir(), "nexterm", "bin")
	lock := binDir + "/.nexterm-" + digest + ".install"
	mkdirErr := errors.New("permission denied")
	host := &installLockHost{
		Host:     &fakeHost{shared: shared, uploadDigest: digest},
		lock:     lock,
		mkdirErr: mkdirErr,
	}
	upload := fakeUploadSource(digest)
	_, err := stageInstallLock(t, host, binDir, upload)
	if err == nil || !strings.Contains(err.Error(), mkdirErr.Error()) {
		t.Fatalf("install error = %v, want %q", err, mkdirErr)
	}
	if host.mkdirCalls != 1 || host.existsCalls != 0 {
		t.Fatalf("lock calls = %d mkdir/%d exists, want 1/0", host.mkdirCalls, host.existsCalls)
	}
	if uploads, _, _ := shared.counts(); uploads != 0 {
		t.Fatalf("uploads = %d after lock failure, want 0", uploads)
	}
}

func missingAncestorTarget(t *testing.T, partial bool) (string, string, *sharedTarget, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "user")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(home, ".local", "share")
	shared := newSharedTarget("")
	shared.singleLevel = true
	shared.dirs[home] = true
	if partial {
		shared.dirs[filepath.Join(home, ".local")] = true
	}
	return dataRoot, home, shared, strings.Repeat("a5", 32)
}

func missingAncestorHost(dataRoot string, shared *sharedTarget, digest string) *fakeHost {
	return &fakeHost{
		discovery:    dataRoot + "\nLinux\n" + runtime.GOARCH + "\n" + strconv.Itoa(os.Geteuid()) + "\n",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		shared:       shared,
		uploadDigest: digest,
	}
}

func missingAncestorResolver(host *fakeHost, shared *sharedTarget, digest string) *Resolver {
	resolver := testResolver(host, "")
	resolver.HelperExecutable = "/offline/nexterm-helper"
	resolver.inspect = func(path string) (UploadSource, error) {
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	resolver.clientFor = func(*Daemon) remoteDaemonClient {
		client := provenFakeDaemonClient(digest)
		client.list = func(context.Context) ([]supervisor.Info, error) {
			running, _ := shared.state()
			if !running {
				return nil, errors.New("daemon is not running")
			}
			return nil, nil
		}
		return client
	}
	return resolver
}

func TestEnsureDaemonCreatesMissingDataHomeAncestors(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "home exists"
		if partial {
			name = "dot local exists"
		}
		t.Run(name, func(t *testing.T) {
			dataRoot, home, shared, digest := missingAncestorTarget(t, partial)
			host := missingAncestorHost(dataRoot, shared, digest)
			daemon, err := missingAncestorResolver(host, shared, digest).EnsureDaemon(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, directory := range []string{
				filepath.Join(home, ".local"), dataRoot, dataRoot + "/nexterm", dataRoot + "/nexterm/bin", dataRoot + "/nexterm/daemon",
			} {
				if !shared.dirs[directory] {
					t.Fatalf("directory %s was not created; dirs=%v", directory, shared.dirs)
				}
			}
			for _, userDirectory := range []string{home, filepath.Join(home, ".local"), dataRoot} {
				if _, changed := host.chmods[userDirectory]; changed {
					t.Fatalf("chmod changed user parent %s: %v", userDirectory, host.chmods)
				}
			}
			for _, privateDirectory := range []string{dataRoot + "/nexterm", dataRoot + "/nexterm/bin", dataRoot + "/nexterm/daemon"} {
				if host.chmods[privateDirectory] != 0o700 {
					t.Fatalf("private directory %s mode = %v", privateDirectory, host.chmods[privateDirectory])
				}
			}
			if daemon.StateDir != dataRoot+"/nexterm/daemon" || len(host.uploads) != 1 {
				t.Fatalf("daemon = %+v, uploads = %v", daemon, host.uploads)
			}
			if _, renames, spawns := shared.counts(); renames != 1 || spawns != 1 {
				t.Fatalf("renames/spawns = %d/%d, want 1/1", renames, spawns)
			}
		})
	}
}

func TestEnsureDaemonConcurrentMissingAncestorCreation(t *testing.T) {
	dataRoot, _, shared, digest := missingAncestorTarget(t, false)
	shared.uploadStarted = make(chan struct{})
	shared.uploadRelease = make(chan struct{})
	shared.installContended = make(chan struct{})
	const clients = 4
	daemons := make([]*Daemon, clients)
	errs := make([]error, clients)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for index := 0; index < clients; index++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			host := missingAncestorHost(dataRoot, shared, digest)
			daemons[index], errs[index] = missingAncestorResolver(host, shared, digest).EnsureDaemon(context.Background(), nil)
		}(index)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(shared.uploadRelease) }) }
	defer release()
	start.Done()
	select {
	case <-shared.uploadStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	select {
	case <-shared.installContended:
	case <-time.After(5 * time.Second):
		t.Fatal("no installation contention")
	}
	release()
	done.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ensure %d: %v", index, err)
		}
		if daemons[index].StateDir != dataRoot+"/nexterm/daemon" {
			t.Fatalf("daemon %d state = %q", index, daemons[index].StateDir)
		}
	}
	uploads, renames, _ := shared.counts()
	if uploads != 1 || renames != 1 {
		t.Fatalf("concurrent uploads/renames = %d/%d, want 1/1", uploads, renames)
	}
}

func TestEnsureTargetDirectoriesRejectsUnsafeExistingPaths(t *testing.T) {
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: "/home/user/.local/share", Identity: "501"}
	paths, err := target.Paths()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"other", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			host := &fakeHost{directoryKinds: map[string]string{paths.Root: kind}}
			if err := ensureTargetDirectories(t.Context(), host, target, paths); err == nil {
				t.Fatalf("accepted %s at NexTerm root", kind)
			}
			if len(host.uploads) != 0 || len(host.chmods) != 0 {
				t.Fatalf("unsafe path caused upload/chmod: %v %v", host.uploads, host.chmods)
			}
		})
	}
	t.Run("permission", func(t *testing.T) {
		host := &fakeHost{
			directoryKinds: map[string]string{paths.Root: "missing", paths.BinDir: "missing"},
			mkdirErrors:    map[string]error{paths.Root: fs.ErrPermission},
		}
		if err := ensureTargetDirectories(t.Context(), host, target, paths); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("permission error = %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		host := &fakeHost{}
		if err := ensureTargetDirectories(ctx, host, target, paths); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		if len(host.mkdirs) != 0 {
			t.Fatalf("cancelled preparation created %v", host.mkdirs)
		}
	})
}

func TestValidateFallbackBaseRejectsSymlinkAndWrongOwner(t *testing.T) {
	t.Run("valid private user directory", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(base, 0o700); err != nil {
			t.Fatal(err)
		}
		location := deploymentLocation{target: Target{GOOS: runtime.GOOS}, base: base, temporary: true}
		if err := validateFallbackBase(t.Context(), spawnProcessHost{fakeHost: &fakeHost{}}, location); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("preexisting symlink", func(t *testing.T) {
		directory := t.TempDir()
		actual := filepath.Join(directory, "actual")
		link := filepath.Join(directory, "link")
		if err := os.Mkdir(actual, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(actual, link); err != nil {
			t.Fatal(err)
		}
		location := deploymentLocation{target: Target{GOOS: runtime.GOOS}, base: link, temporary: true}
		if err := validateFallbackBase(t.Context(), spawnProcessHost{fakeHost: &fakeHost{}}, location); !errors.Is(err, errUnsafeTemporaryRoot) {
			t.Fatalf("symlink validation = %v", err)
		}
	})
	t.Run("wrong owner", func(t *testing.T) {
		directory := t.TempDir()
		base := filepath.Join(directory, "private")
		if err := os.Mkdir(base, 0o700); err != nil {
			t.Fatal(err)
		}
		stat := "#!/bin/sh\nprintf '" + strconv.Itoa(os.Geteuid()+1) + " 700\\n'\n"
		if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(stat), 0o700); err != nil {
			t.Fatal(err)
		}
		location := deploymentLocation{target: Target{GOOS: runtime.GOOS}, base: base, temporary: true}
		host := spawnProcessHost{fakeHost: &fakeHost{}, path: directory}
		if err := validateFallbackBase(t.Context(), host, location); !errors.Is(err, errUnsafeTemporaryRoot) {
			t.Fatalf("wrong-owner validation = %v", err)
		}
	})
}

func TestEnsureDaemonConcurrentPrivateTemporaryCreation(t *testing.T) {
	dataRoot, _, shared, digest := missingAncestorTarget(t, false)
	uid := strconv.Itoa(os.Geteuid())
	shared.dirs["/tmp"] = true
	shared.mkdirErrors = map[string]error{dataRoot + "/nexterm": fs.ErrPermission}
	const clients = 4
	daemons := make([]*Daemon, clients)
	errs := make([]error, clients)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for index := 0; index < clients; index++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			host := missingAncestorHost(dataRoot, shared, digest)
			daemons[index], errs[index] = missingAncestorResolver(host, shared, digest).EnsureDaemon(context.Background(), nil)
		}(index)
	}
	start.Done()
	done.Wait()
	wantState := "/tmp/nexterm-" + uid + "/nexterm/daemon"
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ensure %d: %v", index, err)
		}
		if daemons[index].StateDir != wantState || !daemons[index].Temporary {
			t.Fatalf("daemon %d = %+v, want shared private temporary state %q", index, daemons[index], wantState)
		}
	}
	uploads, renames, _ := shared.counts()
	if uploads != 1 || renames != 1 {
		t.Fatalf("temporary concurrent uploads/renames = %d/%d, want 1/1", uploads, renames)
	}
}
