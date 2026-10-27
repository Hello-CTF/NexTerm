package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func translateError(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return durable.ErrNotFound
	case errors.Is(err, ErrAlreadyExists):
		return durable.ErrAlreadyExists
	case errors.Is(err, ErrInvalidInput):
		return durable.ErrInvalidInput
	case errors.Is(err, ErrIdentity):
		return durable.ErrIdentity
	case errors.Is(err, ErrExited):
		return durable.ErrExited
	case errors.Is(err, ErrClosed):
		return durable.ErrClosed
	case errors.Is(err, ErrGenerationMismatch):
		return durable.ErrGenerationMismatch
	case errors.Is(err, ErrLimit):
		// Keep the helper's message: it names the limit that was reached.
		return fmt.Errorf("%w: %w", durable.ErrLimit, err)
	case errors.Is(err, ErrHelperUpgradeRequired), errors.Is(err, ErrClientUpgradeRequired):
		// Keep the readable reason: it names the side that must be upgraded.
		return fmt.Errorf("%w: %w", durable.ErrUnavailable, err)
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrProtocol), errors.Is(err, ErrUnsupported):
		return durable.ErrUnavailable
	default:
		return err
	}
}

func translateMetadataError(err error) error {
	var conflict *MetadataConflictError
	if errors.As(err, &conflict) {
		return &base.DurableMetadataConflictError{
			CurrentTitle:            conflict.Current.Title,
			CurrentMetadataRevision: conflict.Current.MetadataRevision,
		}
	}
	if errors.Is(err, ErrUnsupported) {
		return base.ErrUnsupported
	}
	return translateError(err)
}

func checkedIdentity(identity base.DurableIdentity) (*Identity, error) {
	if identity.Incarnation == "" {
		return nil, durable.ErrInvalidInput
	}
	return &Identity{CreatedAt: identity.CreatedAt, Incarnation: identity.Incarnation, Generation: identity.Generation}, nil
}

func renameResult(info Info) base.DurableRenameResult {
	return base.DurableRenameResult{Title: info.Title, MetadataRevision: info.MetadataRevision}
}

func renameRequest(options base.DurableRenameOptions) RenameRequest {
	return RenameRequest{
		ID:                       options.ID,
		Incarnation:              options.Incarnation,
		RequestID:                options.RequestID,
		ExpectedMetadataRevision: options.ExpectedMetadataRevision,
		Title:                    options.Title,
	}
}

type Provider struct {
	supervisor *Supervisor
	attach     func(context.Context, *Supervisor, *Session) (*Attachment, error)
}

func embeddedProviderAttach(ctx context.Context, supervisor *Supervisor, session *Session) (*Attachment, error) {
	identity := session.Identity()
	return supervisor.attachFrom(ctx, session.ID(), AttachOptions{}, &identity, "")
}

func NewProvider(supervisor *Supervisor) *Provider {
	if supervisor == nil {
		return nil
	}
	return &Provider{supervisor: supervisor, attach: embeddedProviderAttach}
}

func (p *Provider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	session, err := p.supervisor.Create(ctx, CreateOptions{
		ID:      options.ID,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,

		HistoryLimit: options.HistoryLimit,
	})
	if session == nil {
		return nil, translateError(err)
	}
	attachment, err := p.attach(ctx, p.supervisor, session)
	if err != nil {
		identity := session.Identity()
		_ = p.supervisor.kill(context.Background(), session.ID(), &identity)
		return nil, translateError(err)
	}
	return &channelAttachment{Attachment: attachment}, nil
}

func (p *Provider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.AttachFromWithIdentity(ctx, id, identity, 0)
}

func (p *Provider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	return p.attachWith(ctx, id, identity, AttachOptions{Offset: offset})
}

func (p *Provider) AttachTailWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, options base.DurableTailOptions) (base.DurableAttachment, error) {
	return p.attachWith(ctx, id, identity, tailAttachOptions(options))
}

func (p *Provider) attachWith(ctx context.Context, id string, identity base.DurableIdentity, options AttachOptions) (base.DurableAttachment, error) {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return nil, err
	}
	attachment, err := p.supervisor.attachFrom(ctx, id, options, expected, identity.Generation)
	if attachment == nil {
		return nil, translateError(err)
	}
	return &channelAttachment{Attachment: attachment}, nil
}

func (p *Provider) ReadDurableHistory(ctx context.Context, id string, identity base.DurableIdentity, request base.DurableHistoryRequest) (base.DurableHistoryChunk, error) {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return base.DurableHistoryChunk{}, err
	}
	chunk, err := p.supervisor.readHistory(ctx, id, request.Before, request.MaxBytes, *expected, identity.Generation)
	if err != nil {
		return base.DurableHistoryChunk{}, translateError(err)
	}
	return durableHistoryChunk(chunk), nil
}

func tailAttachOptions(options base.DurableTailOptions) AttachOptions {
	tail := options.Tail
	if tail == 0 {
		tail = DefaultTailBytes
	}
	attach := AttachOptions{Tail: tail}
	if options.Retain != nil {
		attach.Offset = *options.Retain
		attach.Retain = true
	}
	return attach
}

func durableHistoryChunk(chunk HistoryChunk) base.DurableHistoryChunk {
	return base.DurableHistoryChunk{
		Start: chunk.Start, End: chunk.End, HistoryBase: chunk.HistoryBase, HistoryNext: chunk.HistoryNext,
		Truncated: chunk.Truncated, Data: chunk.Data,
	}
}

func durableReplay(replay ReplayInfo) base.DurableReplay {
	return base.DurableReplay{
		HistoryBase: replay.HistoryBase, HistoryNext: replay.HistoryNext, ReplayFrom: replay.ReplayFrom,
		DisplayFrom: replay.DisplayFrom, Truncated: replay.Truncated, Prelude: replay.Prelude,
	}
}

func (p *Provider) KillWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) error {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return err
	}
	return translateError(p.supervisor.kill(ctx, id, expected))
}

func (p *Provider) DurableMetadataSupported(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (p *Provider) RenameDurable(ctx context.Context, options base.DurableRenameOptions) (base.DurableRenameResult, error) {
	info, err := p.supervisor.Rename(ctx, renameRequest(options))
	if err != nil {
		return base.DurableRenameResult{}, translateMetadataError(err)
	}
	return renameResult(info), nil
}

func (p *Provider) ListDurable(ctx context.Context) ([]string, error) {
	infos, err := p.supervisor.List(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	ids := make([]string, len(infos))
	for index, info := range infos {
		ids[index] = info.ID
	}
	return ids, nil
}

// ListDurableDescriptors carries each session's state without its events,
// which polling callers do not read.
func (p *Provider) ListDurableDescriptors(ctx context.Context) ([]base.DurableDescriptor, error) {
	infos, err := p.supervisor.listCanonicalWith(ctx, listOptions{})
	if err != nil {
		return nil, translateError(err)
	}
	return durableDescriptors(infos), nil
}

func durableDescriptors(infos []Info) []base.DurableDescriptor {
	descriptors := make([]base.DurableDescriptor, len(infos))
	for index, info := range infos {
		descriptors[index] = base.DurableDescriptor{
			ID:               info.ID,
			Incarnation:      info.Incarnation,
			Generation:       info.Generation,
			CreatedAt:        info.CreatedAt,
			Title:            info.Title,
			MetadataRevision: info.MetadataRevision,
			Cols:             info.Cols,
			Rows:             info.Rows,
			Dead:             info.Dead,
			Signal:           info.Signal,
			HistoryBase:      info.HistoryBase,
			HistoryNext:      info.HistoryNext,
			HistoryTruncated: info.HistoryTruncated,
			State:            info.State,
		}
		if info.ExitCode != nil {
			exitCode := *info.ExitCode
			descriptors[index].ExitCode = &exitCode
		}
		if info.FinishedAt != nil {
			finishedAt := *info.FinishedAt
			descriptors[index].FinishedAt = &finishedAt
		}
	}
	return descriptors
}

type channelAttachment struct {
	*Attachment
}

func (a *channelAttachment) Stderr() io.Reader  { return nil }
func (a *channelAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *channelAttachment) Generation() uint64 { return 0 }
func (a *channelAttachment) Info() Info         { return a.session.canonicalInfo() }

func (a *channelAttachment) DurableReplay() base.DurableReplay { return durableReplay(a.Replay()) }
func (a *channelAttachment) DurableOffset() uint64             { return a.NextSeq() }

func (a *channelAttachment) Read(p []byte) (int, error) {
	count, err := a.Attachment.Read(p)
	return count, translateError(err)
}

func (a *channelAttachment) Write(p []byte) (int, error) {
	count, err := a.Attachment.Write(p)
	return count, translateError(err)
}

func (a *channelAttachment) Resize(ctx context.Context, cols, rows uint32) error {
	return translateError(a.Attachment.Resize(ctx, cols, rows))
}

func (a *channelAttachment) Wait(ctx context.Context) error {
	return translateError(a.Attachment.Wait(ctx))
}

func (a *channelAttachment) Kill(ctx context.Context) error {
	return translateError(a.Attachment.Kill(ctx))
}

func (a *channelAttachment) Close() error {
	return translateError(a.Attachment.Close())
}

func (a *channelAttachment) DurableVersions() (eventVersion, gridRevision uint64, err error) {
	event, grid, err := a.Versions()
	return event, grid, translateError(err)
}

func (a *channelAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	return translateError(a.PersistVersions(eventVersion, gridRevision))
}

func (a *channelAttachment) DurableGrid() (cols, rows uint32, ok bool) {
	info := a.Info()
	if info.Cols == 0 || info.Rows == 0 {
		return 0, 0, false
	}
	return info.Cols, info.Rows, true
}

type RemoteProvider struct {
	client *Client
	create func(context.Context, *Client, CreateOptions) (Info, error)
	attach func(context.Context, *Client, string, *Identity) (*Stream, error)
	// createAttach, when set, creates and attaches on one connection. Tests
	// that replace create or attach clear it so Create attaches separately.
	createAttach func(context.Context, *Client, CreateOptions) (Info, *Stream, error)
}

func NewRemoteProvider(client *Client) *RemoteProvider {
	if client == nil {
		return nil
	}
	return &RemoteProvider{client: client, create: remoteProviderCreate, attach: remoteProviderAttach, createAttach: remoteProviderCreateAttach}
}

func remoteProviderCreate(ctx context.Context, client *Client, options CreateOptions) (Info, error) {
	return client.Create(ctx, options)
}

func remoteProviderCreateAttach(ctx context.Context, client *Client, options CreateOptions) (Info, *Stream, error) {
	return client.CreateAttach(ctx, options)
}

func remoteProviderAttach(ctx context.Context, client *Client, id string, expect *Identity) (*Stream, error) {
	return client.Attach(ctx, id, expect)
}

func (p *RemoteProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	if options.ID == "" {
		options.ID = ids.New()
	}
	attempt := ids.New()
	createOptions := CreateOptions{
		ID:      options.ID,
		Attempt: attempt,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,

		HistoryLimit: options.HistoryLimit,
	}
	var info Info
	var stream *Stream
	var err error
	if p.createAttach != nil {
		info, stream, err = p.createAttach(ctx, p.client, createOptions)
	} else {
		info, err = p.create(ctx, p.client, createOptions)
	}
	if err != nil {
		switch {
		case info.ID != "":
			// Created but not attached: remove the session by its identity.
			p.compensateCreate(info.ID, &Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation})
		case !definiteCreateRejection(err):
			p.compensateCreateAttempt(options.ID, attempt)
		}
		return nil, translateError(err)
	}
	if stream != nil {
		return &streamAttachment{Stream: stream, provider: p}, nil
	}
	expect := &Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation, Generation: info.Generation}
	stream, err = p.attach(ctx, p.client, info.ID, expect)
	if err != nil {
		p.compensateCreate(info.ID, expect)
		return nil, translateError(err)
	}
	return &streamAttachment{Stream: stream, provider: p}, nil
}

func definiteCreateRejection(err error) bool {
	var wire *wireError
	return errors.As(err, &wire)
}

func (p *RemoteProvider) compensateCreate(id string, expected *Identity) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = p.client.Kill(cleanupCtx, id, expected)
}

func (p *RemoteProvider) compensateCreateAttempt(id, attempt string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = p.client.ReconcileCreate(cleanupCtx, id, attempt)
}

func (p *RemoteProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.AttachFromWithIdentity(ctx, id, identity, 0)
}

func (p *RemoteProvider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.AttachFrom(ctx, id, expected, offset)
	if err != nil {
		return nil, translateError(err)
	}
	return &streamAttachment{Stream: stream, provider: p}, nil
}

func (p *RemoteProvider) AttachTailWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, options base.DurableTailOptions) (base.DurableAttachment, error) {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.AttachWithOptions(ctx, id, expected, tailAttachOptions(options))
	if err != nil {
		return nil, translateError(err)
	}
	return &streamAttachment{Stream: stream, provider: p}, nil
}

func (p *RemoteProvider) ReadDurableHistory(ctx context.Context, id string, identity base.DurableIdentity, request base.DurableHistoryRequest) (base.DurableHistoryChunk, error) {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return base.DurableHistoryChunk{}, err
	}
	chunk, err := p.client.ReadHistory(ctx, id, *expected, request.Before, request.MaxBytes)
	if err != nil {
		return base.DurableHistoryChunk{}, translateError(err)
	}
	return durableHistoryChunk(chunk), nil
}

func (p *RemoteProvider) KillWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) error {
	expected, err := checkedIdentity(identity)
	if err != nil {
		return err
	}
	return translateError(p.client.Kill(ctx, id, expected))
}

func (p *RemoteProvider) DurableMetadataSupported(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (p *RemoteProvider) RenameDurable(ctx context.Context, options base.DurableRenameOptions) (base.DurableRenameResult, error) {
	info, err := p.client.Rename(ctx, renameRequest(options))
	if err != nil {
		return base.DurableRenameResult{}, translateMetadataError(err)
	}
	return renameResult(info), nil
}

func (p *RemoteProvider) ListDurable(ctx context.Context) ([]string, error) {
	infos, err := p.client.List(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	ids := make([]string, len(infos))
	for index, info := range infos {
		ids[index] = info.ID
	}
	return ids, nil
}

// ListDurableDescriptors carries each session's state without its events,
// which polling callers do not read.
func (p *RemoteProvider) ListDurableDescriptors(ctx context.Context) ([]base.DurableDescriptor, error) {
	infos, err := p.client.List(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return durableDescriptors(infos), nil
}

type streamAttachment struct {
	*Stream
	provider *RemoteProvider
}

func (a *streamAttachment) Stderr() io.Reader  { return nil }
func (a *streamAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *streamAttachment) Generation() uint64 { return 0 }
func (a *streamAttachment) ID() string         { return a.Info().ID }

func (a *streamAttachment) DurableReplay() base.DurableReplay { return durableReplay(a.Replay()) }
func (a *streamAttachment) DurableOffset() uint64             { return a.ConsumedOffset() }

func (a *streamAttachment) Read(p []byte) (int, error) {
	count, err := a.Stream.Read(p)
	return count, translateError(err)
}

func (a *streamAttachment) Write(p []byte) (int, error) {
	count, err := a.Stream.Write(p)
	return count, translateError(err)
}

func (a *streamAttachment) Resize(ctx context.Context, cols, rows uint32) error {
	return translateError(a.Stream.Resize(ctx, cols, rows))
}

func (a *streamAttachment) Wait(ctx context.Context) error {
	return translateError(a.Stream.Wait(ctx))
}

func (a *streamAttachment) Kill(ctx context.Context) error {
	err := a.Stream.Kill(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrClosed) && !errors.Is(err, base.ErrDisconnected) {
		return translateError(err)
	}
	identity := a.Identity()
	if killErr := a.provider.client.Kill(ctx, a.Info().ID, &identity); killErr != nil {
		return translateError(killErr)
	}
	return nil
}

func (a *streamAttachment) Close() error {
	return translateError(a.Stream.Close())
}

func (a *streamAttachment) DurableVersions() (eventVersion, gridRevision uint64, err error) {
	event, grid, err := a.Versions()
	return event, grid, translateError(err)
}

func (a *streamAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	return translateError(a.PersistVersions(eventVersion, gridRevision))
}

func (a *streamAttachment) DurableGrid() (cols, rows uint32, ok bool) {
	info := a.Info()
	if info.Cols == 0 || info.Rows == 0 {
		return 0, 0, false
	}
	return info.Cols, info.Rows, true
}

var (
	_ base.DurableProvider         = (*Provider)(nil)
	_ base.DurableDescriptorLister = (*Provider)(nil)
	_ base.DurableMetadataProvider = (*Provider)(nil)
	_ base.DurableIdentityAttacher = (*Provider)(nil)
	_ base.DurableIdentityKiller   = (*Provider)(nil)
	_ base.DurableTailAttacher     = (*Provider)(nil)
	_ base.DurableHistoryReader    = (*Provider)(nil)
	_ base.DurableAttachment       = (*channelAttachment)(nil)
	_ base.DurableReplayReporter   = (*channelAttachment)(nil)
	_ base.DurableProvider         = (*RemoteProvider)(nil)
	_ base.DurableDescriptorLister = (*RemoteProvider)(nil)
	_ base.DurableMetadataProvider = (*RemoteProvider)(nil)
	_ base.DurableIdentityAttacher = (*RemoteProvider)(nil)
	_ base.DurableIdentityKiller   = (*RemoteProvider)(nil)
	_ base.DurableTailAttacher     = (*RemoteProvider)(nil)
	_ base.DurableHistoryReader    = (*RemoteProvider)(nil)
	_ base.DurableAttachment       = (*streamAttachment)(nil)
	_ base.DurableReplayReporter   = (*streamAttachment)(nil)
)
