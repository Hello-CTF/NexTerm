package supervisor

import (
	"context"
	"io"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

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
		return nil, err
	}
	attachment, err := session.attach()
	if err != nil {
		_ = p.supervisor.Kill(context.Background(), session.ID())
		return nil, err
	}
	return &channelAttachment{Attachment: attachment}, nil
}

func (p *Provider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	attachment, err := p.supervisor.Attach(ctx, id)
	if attachment == nil {
		return nil, err
	}
	return &channelAttachment{Attachment: attachment}, nil
}

type channelAttachment struct {
	*Attachment
}

func (a *channelAttachment) Stderr() io.Reader  { return nil }
func (a *channelAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *channelAttachment) Generation() uint64 { return 0 }
func (a *channelAttachment) DurableVersions() (eventVersion, gridRevision uint64, err error) {
	return a.Versions()
}

func (a *channelAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	return a.PersistVersions(eventVersion, gridRevision)
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

func (p *RemoteProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	info, err := p.client.Create(ctx, CreateOptions{
		ID:      options.ID,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,
	})
	if err != nil {
		return nil, err
	}
	stream, err := p.client.Attach(ctx, info.ID, nil)
	if err != nil {
		return nil, err
	}
	return &streamAttachment{Stream: stream}, nil
}

func (p *RemoteProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	stream, err := p.client.Attach(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	return &streamAttachment{Stream: stream}, nil
}

type streamAttachment struct {
	*Stream
}

func (a *streamAttachment) Stderr() io.Reader  { return nil }
func (a *streamAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *streamAttachment) Generation() uint64 { return 0 }
func (a *streamAttachment) ID() string         { return a.Info().ID }

func (a *streamAttachment) DurableVersions() (eventVersion, gridRevision uint64, err error) {
	return a.Versions()
}

func (a *streamAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	return a.PersistVersions(eventVersion, gridRevision)
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
