package production

import (
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// localHelperInstanceID reports the instance id of the local helper in its
// current state directory, empty while the helper is not running. It pairs
// with localHelperDigest to tell this helper from another machine's.
func localHelperInstanceID(helper *localhelper.Service) func() string {
	if helper == nil {
		return nil
	}
	return func() string {
		if state := helper.Status().State; state != localhelper.StateReady && state != localhelper.StateOutdated {
			return ""
		}
		id, err := supervisor.HelperInstanceID(helper.StateDir())
		if err != nil {
			return ""
		}
		return id
	}
}
