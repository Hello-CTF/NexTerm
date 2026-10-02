package forward

import "github.com/ProbiusOfficial/NexTerm/internal/session"

func providerIPCError(err error) error {
	return session.IPCError(err)
}
