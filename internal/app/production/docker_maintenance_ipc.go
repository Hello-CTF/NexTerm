package production

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type dockerComposeActionIPCRequest struct {
	SessionID              string                        `json:"sessionId"`
	Project                string                        `json:"project"`
	Action                 docker.ComposeAction          `json:"action"`
	ProductionConfirmation *dockerProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type dockerComposeLogsIPCRequest struct {
	SessionID string `json:"sessionId"`
	Project   string `json:"project"`
	Tail      *int   `json:"tail,omitempty"`
}

type dockerComposeRedeployIPCRequest struct {
	SessionID              string                        `json:"sessionId"`
	Project                string                        `json:"project"`
	Path                   string                        `json:"path"`
	BaseContentBase64      string                        `json:"baseContentBase64"`
	ContentBase64          string                        `json:"contentBase64"`
	Ticket                 string                        `json:"ticket"`
	ProductionConfirmation *dockerProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type dockerComposeRedeployPrepareResult struct {
	Ticket      string `json:"ticket"`
	ContentHash string `json:"contentHash"`
}

type dockerPrunePreviewIPCRequest struct {
	SessionID string             `json:"sessionId"`
	Kinds     []docker.PruneKind `json:"kinds"`
}

type dockerPruneIPCRequest struct {
	SessionID              string                        `json:"sessionId"`
	Token                  string                        `json:"token"`
	Digest                 string                        `json:"digest"`
	ProductionConfirmation *dockerProductionConfirmation `json:"productionConfirmation,omitempty"`
}

func (s *terminalCommandService) registerDockerMaintenance(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "docker_compose_stacks", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				if _, err := s.authorizeDockerSession(ctx, input.SessionID); err != nil {
					return nil, terminalIPCError(err)
				}
				value, err := s.docker.ComposeStacks(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_compose_action", func(ctx context.Context, _ *ipc.Call, input dockerComposeActionIPCRequest) (any, error) {
				connected, err := s.authorizeDockerSession(ctx, input.SessionID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				if input.Action == docker.ComposeActionDown {
					if _, err := s.docker.ComposeStack(ctx, input.SessionID, input.Project); err != nil {
						return nil, terminalIPCError(err)
					}
					operation := dockerMutationOperation{Kind: "compose_down", Project: input.Project}
					if err := s.guardDockerMutation(ctx, connected, operation, input.ProductionConfirmation); err != nil {
						return nil, dockerMutationIPCError(err)
					}
				}
				value, err := s.docker.ComposeAction(ctx, docker.ComposeActionRequest{
					SessionID: input.SessionID, AssetID: connected.Asset().ID, Source: "user", Project: input.Project, Action: input.Action,
				})
				return value, dockerMutationIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_compose_logs_attach", func(ctx context.Context, call *ipc.Call, input dockerComposeLogsIPCRequest) (string, error) {
				if _, err := s.authorizeDockerSession(ctx, input.SessionID); err != nil {
					return "", terminalIPCError(err)
				}
				stream, sink, err := s.openDockerSink(ctx, call)
				if err != nil {
					return "", err
				}
				id, err := s.docker.AttachCompose(ctx, docker.ComposeAttachRequest{
					SessionID: input.SessionID, Project: input.Project, Action: docker.ComposeActionLogs, Tail: input.Tail, Sink: sink,
				})
				if err != nil {
					_ = stream.Close()
					return "", terminalIPCError(err)
				}
				s.watchDockerStream(id, stream)
				return id, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_compose_redeploy_prepare", func(ctx context.Context, _ *ipc.Call, input dockerComposeRedeployIPCRequest) (any, error) {
				value, err := s.composeRedeployWrite(ctx, input)
				return value, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_compose_redeploy_attach", func(ctx context.Context, call *ipc.Call, input dockerComposeRedeployIPCRequest) (string, error) {
				connected, _, ticket, err := s.authorizeComposeRedeployAttach(ctx, input)
				if err != nil {
					return "", err
				}
				stream, sink, err := s.openDockerSink(ctx, call)
				if err != nil {
					return "", err
				}
				id, err := s.docker.AttachCompose(ctx, docker.ComposeAttachRequest{
					SessionID: input.SessionID, AssetID: connected.Asset().ID, Source: "user", Project: input.Project,
					Action: docker.ComposeActionUp, ContentHash: ticket.operation.ContentHash, Sink: sink,
				})
				if err != nil {
					_ = stream.Close()
					return "", terminalIPCError(err)
				}
				s.watchDockerStream(id, stream)
				return id, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_disk_usage", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				if _, err := s.authorizeDockerSession(ctx, input.SessionID); err != nil {
					return nil, terminalIPCError(err)
				}
				value, err := s.docker.DiskUsage(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_prune_preview", func(ctx context.Context, _ *ipc.Call, input dockerPrunePreviewIPCRequest) (any, error) {
				if _, err := s.authorizeDockerSession(ctx, input.SessionID); err != nil {
					return nil, terminalIPCError(err)
				}
				value, err := s.docker.PreviewPrune(ctx, input.SessionID, input.Kinds)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_prune", func(ctx context.Context, _ *ipc.Call, input dockerPruneIPCRequest) (any, error) {
				connected, err := s.authorizeDockerSession(ctx, input.SessionID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				operation := dockerMutationOperation{Kind: "prune", PreviewToken: input.Token, PreviewDigest: input.Digest}
				if err := s.guardDockerMutation(ctx, connected, operation, input.ProductionConfirmation); err != nil {
					return nil, dockerMutationIPCError(err)
				}
				value, err := s.docker.Prune(ctx, docker.PruneRequest{
					SessionID: input.SessionID, AssetID: connected.Asset().ID, Source: "user", Token: input.Token, Digest: input.Digest,
				})
				if err != nil {
					s.docker.InvalidatePrunePreview(input.SessionID, input.Token)
				}
				return value, dockerMutationIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_reclaimable", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				if _, err := s.authorizeDockerSession(ctx, input.SessionID); err != nil {
					return nil, terminalIPCError(err)
				}
				value, err := s.docker.Reclaimable(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func (s *terminalCommandService) authorizeComposeRedeploy(ctx context.Context, input dockerComposeRedeployIPCRequest) (*session.Session, docker.ComposeStack, error) {
	connected, err := s.authorizeDockerSession(ctx, input.SessionID)
	if err != nil {
		return nil, docker.ComposeStack{}, terminalIPCError(err)
	}
	stack, err := s.docker.ComposeStack(ctx, input.SessionID, input.Project)
	if err != nil {
		return nil, docker.ComposeStack{}, terminalIPCError(err)
	}
	if !composeEditPathAllowed(stack, input.Path) {
		return nil, docker.ComposeStack{}, ipc.BadParam(errors.New("compose 编辑路径无效"))
	}
	return connected, stack, nil
}

func (s *terminalCommandService) composeRedeployWrite(ctx context.Context, input dockerComposeRedeployIPCRequest) (any, error) {
	connected, _, err := s.authorizeComposeRedeploy(ctx, input)
	if err != nil {
		return nil, err
	}
	baseContent, err := base64.StdEncoding.DecodeString(input.BaseContentBase64)
	if err != nil {
		return nil, ipc.BadParam(errors.New("compose 磁盘基线不是有效的 Base64"))
	}
	content, err := base64.StdEncoding.DecodeString(input.ContentBase64)
	if err != nil {
		return nil, ipc.BadParam(errors.New("compose 新内容不是有效的 Base64"))
	}
	if len(baseContent) > fsReadDefaultMaxBytes || len(content) > fsReadDefaultMaxBytes {
		return nil, ipc.BadParam(errors.New("compose 文件超过允许的大小"))
	}
	transport, err := s.sessions.TransportFor(productionSessionRequestContext(ctx), input.SessionID)
	if err != nil {
		return nil, terminalIPCError(err)
	}
	provider, ok := transport.(base.FileTransport)
	if !ok {
		return nil, terminalIPCError(base.ErrUnsupported)
	}
	filesystem, err := provider.FileSystem(ctx)
	if err != nil {
		return nil, fsIPCError(transport.Kind(), err)
	}
	current, err := filesystem.ReadFile(ctx, input.Path, fsReadDefaultMaxBytes)
	if err != nil {
		return nil, fsIPCError(transport.Kind(), err)
	}
	if !bytes.Equal(current, baseContent) {
		return nil, ipc.BadParam(errors.New("compose 文件在确认前已被外部修改，请重新读取并合并"))
	}
	operation := dockerMutationOperation{
		Kind: "compose_redeploy", Project: input.Project, Path: input.Path,
		BaseContentHash: dockerConfirmationDigest("base-content-v1", baseContent),
		ContentHash:     dockerConfirmationDigest("content-v1", content),
	}
	if err := s.guardDockerMutation(ctx, connected, operation, input.ProductionConfirmation); err != nil {
		return nil, dockerMutationIPCError(err)
	}
	if err := filesystem.WriteFile(ctx, input.Path, content, true); err != nil {
		return nil, fsIPCError(transport.Kind(), err)
	}
	ticket, err := issueDockerRedeployTicket(input.SessionID, input.Project, input.Path, operation)
	if err != nil {
		return nil, terminalIPCError(err)
	}
	return dockerComposeRedeployPrepareResult{Ticket: ticket, ContentHash: operation.ContentHash}, nil
}

func (s *terminalCommandService) authorizeComposeRedeployAttach(ctx context.Context, input dockerComposeRedeployIPCRequest) (*session.Session, docker.ComposeStack, dockerRedeployTicket, error) {
	connected, stack, err := s.authorizeComposeRedeploy(ctx, input)
	if err != nil {
		return nil, docker.ComposeStack{}, dockerRedeployTicket{}, err
	}
	ticket, err := consumeDockerRedeployTicket(input.SessionID, input.Project, input.Path, input.Ticket)
	if err != nil {
		return nil, docker.ComposeStack{}, dockerRedeployTicket{}, ipc.BadParam(err)
	}
	return connected, stack, ticket, nil
}

type dockerRedeployTicket struct {
	sessionID string
	project   string
	path      string
	operation dockerMutationOperation
	expiresAt time.Time
}

var dockerRedeployTickets = struct {
	sync.Mutex
	items map[string]dockerRedeployTicket
}{items: make(map[string]dockerRedeployTicket)}

func issueDockerRedeployTicket(sessionID, project, path string, operation dockerMutationOperation) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random[:])
	now := time.Now()
	dockerRedeployTickets.Lock()
	for key, ticket := range dockerRedeployTickets.items {
		if ticket.sessionID == sessionID && !ticket.expiresAt.After(now) {
			delete(dockerRedeployTickets.items, key)
		}
	}
	dockerRedeployTickets.items[token] = dockerRedeployTicket{
		sessionID: sessionID, project: project, path: path, operation: operation, expiresAt: now.Add(5 * time.Minute),
	}
	dockerRedeployTickets.Unlock()
	return token, nil
}

func consumeDockerRedeployTicket(sessionID, project, path, token string) (dockerRedeployTicket, error) {
	dockerRedeployTickets.Lock()
	defer dockerRedeployTickets.Unlock()
	ticket, ok := dockerRedeployTickets.items[token]
	if !ok || ticket.sessionID != sessionID || ticket.project != project || ticket.path != path {
		return dockerRedeployTicket{}, errors.New("redeploy 票据无效或已使用，请重新保存")
	}
	delete(dockerRedeployTickets.items, token)
	if !ticket.expiresAt.After(time.Now()) {
		return dockerRedeployTicket{}, errors.New("redeploy 票据已过期，请重新保存")
	}
	return ticket, nil
}

func composeEditPathAllowed(stack docker.ComposeStack, path string) bool {
	if path == stack.EnvPath && path != "" {
		return true
	}
	for _, file := range stack.ConfigFiles {
		if path == file {
			return true
		}
	}
	return false
}

func validDockerContentHash(value string) bool {
	if len(value) != 64 || strings.TrimSpace(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
