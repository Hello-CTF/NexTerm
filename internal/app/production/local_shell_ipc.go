package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// registerLocalShellProfiles lists the shells a local asset can run on the host that runs local terminals: the
// desktop machine, or the server for a single-user server. A multi-user server refuses local assets, so it
// refuses the listing too rather than describing the server's own shells to its accounts.
func registerLocalShellProfiles(dispatcher *ipc.Dispatcher, restricted func(context.Context) bool) error {
	return ipc.Register(dispatcher, "local_shell_profiles", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]local.ShellProfile, error) {
		if restricted(ctx) {
			return nil, ipc.NewError(ipc.CodeUnsupported, "多用户服务端不支持本地终端资产，无法列出服务器上的 Shell")
		}
		profiles := local.ShellProfiles()
		if profiles == nil {
			profiles = []local.ShellProfile{}
		}
		return profiles, nil
	})
}
