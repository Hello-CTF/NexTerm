package supervisor

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrProtocol), errors.Is(err, ErrUnsupported):
		return durable.ErrUnavailable
	default:
		return err
	}
}

type Provider struct {
	supervisor *Supervisor
}

func NewProvider(supervisor *Supervisor) *Provider {
	if supervisor == nil {
		return nil
	}
	return &Provider{supervisor: supervisor}
}

func (p *Provider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	session, err := p.supervisor.Create(ctx, CreateOptions{
		ID:      options.ID,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,
	})
	if session == nil {
		return nil, translateError(err)
	}
	attachment, err := session.attach()
	if err != nil {
		identity := session.Identity()
		_ = p.supervisor.kill(context.Background(), session.ID(), &identity)
		return nil, translateError(err)
	}
	return &channelAttachment{Attachment: attachment}, nil
}

func (p *Provider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	attachment, err := p.supervisor.Attach(ctx, id)
	if attachment == nil {
		return nil, translateError(err)
	}
	return &channelAttachment{Attachment: attachment}, nil
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

type channelAttachment struct {
	*Attachment
}

func (a *channelAttachment) Stderr() io.Reader  { return nil }
func (a *channelAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *channelAttachment) Generation() uint64 { return 0 }

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
}

func NewRemoteProvider(client *Client) *RemoteProvider {
	if client == nil {
		return nil
	}
	return &RemoteProvider{client: client}
}

var remoteProviderCreate = func(ctx context.Context, client *Client, options CreateOptions) (Info, error) {
	return client.Create(ctx, options)
}

var remoteProviderAttach = func(ctx context.Context, client *Client, id string, expect *Identity) (*Stream, error) {
	return client.Attach(ctx, id, expect)
}

func (p *RemoteProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	if options.ID == "" {
		options.ID = ids.New()
	}
	attempt := ids.New()
	info, err := remoteProviderCreate(ctx, p.client, CreateOptions{
		ID:      options.ID,
		Attempt: attempt,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,
	})
	if err != nil {
		if !definiteCreateRejection(err) {
			p.compensateCreateAttempt(options.ID, attempt)
		}
		return nil, translateError(err)
	}
	expect := &Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation}
	stream, err := remoteProviderAttach(ctx, p.client, info.ID, expect)
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

func (p *RemoteProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	stream, err := p.client.Attach(ctx, id, nil)
	if err != nil {
		return nil, translateError(err)
	}
	return &streamAttachment{Stream: stream, provider: p}, nil
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

type streamAttachment struct {
	*Stream
	provider *RemoteProvider
}

func (a *streamAttachment) Stderr() io.Reader  { return nil }
func (a *streamAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *streamAttachment) Generation() uint64 { return 0 }
func (a *streamAttachment) ID() string         { return a.Info().ID }

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
	_ base.DurableProvider   = (*Provider)(nil)
	_ base.DurableAttachment = (*channelAttachment)(nil)
	_ base.DurableProvider   = (*RemoteProvider)(nil)
	_ base.DurableAttachment = (*streamAttachment)(nil)
)
