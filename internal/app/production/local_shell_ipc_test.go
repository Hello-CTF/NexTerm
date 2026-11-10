package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func TestLocalShellProfilesRefusedWhereLocalAssetsAre(t *testing.T) {
	restricted := false
	dispatcher := ipc.NewDispatcher()
	if err := registerLocalShellProfiles(dispatcher, func(context.Context) bool { return restricted }); err != nil {
		t.Fatal(err)
	}
	var profiles []local.ShellProfile
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "local_shell_profiles", `{}`), &profiles)
	if profiles == nil {
		t.Fatal("local_shell_profiles returned null; want a list, possibly empty")
	}
	for _, profile := range profiles {
		if profile.ID == "" || profile.Name == "" || profile.Path == "" || profile.Args == nil {
			t.Fatalf("incomplete profile %+v", profile)
		}
	}

	restricted = true
	response := dispatchStoreTest(dispatcher, "local_shell_profiles", `{}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("multi-user response = %+v, want unsupported", response)
	}
}
