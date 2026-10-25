package ssh

import (
	"context"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

// knownDeployment is what a previous successful ensure learned about one
// account on one host. It is only a hint: every use re-verifies it against the
// live helper, and any doubt falls back to the full discovery flow.
type knownDeployment struct {
	target Target
	binary string
}

// deploymentMemory remembers the last good deployment per host identity.
type deploymentMemory struct {
	mu      sync.Mutex
	entries map[string]knownDeployment
}

// EnableDeploymentMemory turns on the fast reconnect path for this resolver and every copy made from it
// afterwards. Call it once on the shared resolver, before copies are taken.
func (r *Resolver) EnableDeploymentMemory() {
	if r.deployments == nil {
		r.deployments = &deploymentMemory{}
	}
}

func (m *deploymentMemory) get(key string) (knownDeployment, bool) {
	if m == nil {
		return knownDeployment{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	return entry, ok
}

func (m *deploymentMemory) put(key string, entry knownDeployment) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]knownDeployment)
	}
	m.entries[key] = entry
}

func (m *deploymentMemory) forget(key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
}

// deploymentKey identifies the host and account a remembered deployment belongs
// to. Without a verified host identity nothing is remembered or reused.
func (r *Resolver) deploymentKey(host Host, client *transportssh.Client) string {
	if r.hostIdentity != nil {
		return r.hostIdentity(host, client)
	}
	if client == nil {
		return ""
	}
	return client.HostIdentity()
}

// rememberDeployment records a successful, fully verified result. Daemons in
// temporary roots are not remembered: those roots need re-validation each time.
func (r *Resolver) rememberDeployment(key string, daemon *Daemon) {
	if key == "" || daemon == nil || daemon.Temporary || !daemon.MetadataKnown || daemon.Binary == "" {
		return
	}
	r.deployments.put(key, knownDeployment{target: daemon.Target, binary: daemon.Binary})
}

// fastEnsure reconnects to a daemon this process already verified on the same
// host identity, using two remote operations instead of the full discovery
// walk: one digest check of the helper file (when it is content addressed) and
// one bridge connection whose runtime metadata must prove the same helper
// digest and protocol as the local helper. It returns nil on any doubt, which
// sends the caller down the full flow with all of its refusals.
func (r *Resolver) fastEnsure(ctx context.Context, host Host, client *transportssh.Client, key string) *Daemon {
	if key == "" {
		return nil
	}
	entry, ok := r.deployments.get(key)
	if !ok {
		return nil
	}
	daemon := r.verifyKnownDeployment(ctx, host, client, entry)
	if daemon == nil {
		r.deployments.forget(key)
	}
	return daemon
}

func (r *Resolver) verifyKnownDeployment(ctx context.Context, host Host, client *transportssh.Client, entry knownDeployment) *Daemon {
	paths, err := entry.target.Paths()
	if err != nil {
		return nil
	}
	source, err := r.uploadSourceForTarget(entry.target)
	if err != nil || source == nil || source.ValidateForTarget(entry.target) != nil {
		return nil
	}
	host, err = r.withExecFallback(host, client, entry.target)
	if err != nil {
		return nil
	}
	r.reportProgress(ctx, HelperProgress{Step: 2, Stage: "probe", Path: entry.binary})
	if strings.Contains(entry.binary, strings.ToLower(source.Digest)) {
		// A content-addressed helper must still hold exactly the local bytes.
		digest, err := host.ChecksumSHA256(ctx, entry.binary)
		if err != nil || !strings.EqualFold(digest, source.Digest) {
			return nil
		}
	}
	location := deploymentLocation{target: entry.target, paths: paths}
	daemon := newTargetDaemon(host, location, entry.binary)
	metadataCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	metadata, known, err := r.remoteClient(daemon).RuntimeMetadata(metadataCtx)
	if err != nil || !known || metadata.Protocol != supervisor.ProtocolVersion || !strings.EqualFold(metadata.SHA256, source.Digest) {
		return nil
	}
	daemon.RunningMetadata, daemon.MetadataKnown = metadata, known
	daemon.Update.Local = localHelperMetadata(*source)
	daemon.Update.Running = metadata
	return daemon
}
