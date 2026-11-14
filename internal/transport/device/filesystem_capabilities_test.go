package device

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestFileSystemCapabilitiesFollowAgentPlatform(t *testing.T) {
	for platform, want := range map[string]base.FileCapabilities{
		"":        {Chmod: base.ChmodPOSIX, Pack: true, Extract: true},
		"linux":   {Chmod: base.ChmodPOSIX, Pack: true, Extract: true},
		"darwin":  {Chmod: base.ChmodPOSIX, Pack: true, Extract: true, CaseInsensitive: true},
		"windows": {Chmod: base.ChmodReadOnly, CaseInsensitive: true},
	} {
		script := scriptedFileSystem{
			// The agent's own reply does not decide archive support, which runs through the device shell.
			roots: base.FileSystemRoots{PathStyle: base.PathStylePOSIX, Supported: true, Capabilities: base.FileCapabilities{Chmod: base.ChmodNone}},
		}
		filesystem := remote.NewFileSystem(remote.Options{Dial: script.dial, Platform: platform})
		roots, err := filesystem.Roots(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if roots.Capabilities != want {
			t.Fatalf("platform %q capabilities = %+v, want %+v", platform, roots.Capabilities, want)
		}
	}
}

func TestFileSystemListMarksDotEntriesHiddenForPOSIXAgents(t *testing.T) {
	entries := []base.FileEntry{
		{Name: ".profile", Path: "/home/a/.profile", Kind: base.FileFile},
		{Name: "notes", Path: "/home/a/notes", Kind: base.FileFile},
	}
	for platform, wantDotHidden := range map[string]bool{"linux": true, "windows": false} {
		filesystem := remote.NewFileSystem(remote.Options{Dial: (scriptedFileSystem{entries: entries}).dial, Platform: platform})
		got, err := filesystem.List(context.Background(), "/home/a")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Hidden != wantDotHidden || got[1].Hidden {
			t.Fatalf("platform %q entries = %+v", platform, got)
		}
	}
}
