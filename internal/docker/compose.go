package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
)

const (
	composeProjectLabel     = "com.docker.compose.project"
	composeServiceLabel     = "com.docker.compose.service"
	composeWorkingDirLabel  = "com.docker.compose.project.working_dir"
	composeConfigFilesLabel = "com.docker.compose.project.config_files"
)

type ComposeCLI string

const (
	ComposeCLIPlugin ComposeCLI = "docker compose"
	ComposeCLILegacy ComposeCLI = "docker-compose"
)

type ComposeAction string

const (
	ComposeActionUp      ComposeAction = "up"
	ComposeActionDown    ComposeAction = "down"
	ComposeActionRestart ComposeAction = "restart"
	ComposeActionPull    ComposeAction = "pull"
	ComposeActionLogs    ComposeAction = "logs"
)

type ComposeStack struct {
	Project     string   `json:"project"`
	WorkingDir  string   `json:"workingDir"`
	ConfigFiles []string `json:"configFiles"`
	EnvPath     string   `json:"envPath"`
	EnvExists   bool     `json:"envExists"`
	Services    []string `json:"services"`
	ReadOnly    bool     `json:"readOnly"`
	Reason      string   `json:"reason,omitempty"`
}

type ComposeOverview struct {
	CLI       ComposeCLI     `json:"cli,omitempty"`
	Available bool           `json:"available"`
	Message   string         `json:"message,omitempty"`
	Stacks    []ComposeStack `json:"stacks"`
}

type ComposeFilesStatus struct {
	Missing   []string
	EnvExists bool
}

type ComposeCommandBackend interface {
	ProbeCompose(ctx context.Context, cli ComposeCLI) error
	CheckComposeFiles(ctx context.Context, stack ComposeStack) (ComposeFilesStatus, error)
	RunCompose(ctx context.Context, cli ComposeCLI, stack ComposeStack, action ComposeAction) (string, error)
	OpenCompose(ctx context.Context, cli ComposeCLI, stack ComposeStack, action ComposeAction, tail int) (LogStream, error)
}

type ComposeActionRequest struct {
	SessionID string
	AssetID   string
	Source    string
	Project   string
	Action    ComposeAction
}

type ComposeAttachRequest struct {
	SessionID   string
	AssetID     string
	Source      string
	Project     string
	Action      ComposeAction
	ContentHash string
	Tail        *int
	Sink        FrameSink
}

type composeProbe struct {
	ready chan struct{}
	cli   ComposeCLI
	err   error
}

func (s *Service) ComposeStacks(ctx context.Context, sessionID string) (ComposeOverview, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	stacks, err := s.discoverComposeStacks(ctx, sessionID)
	if err != nil {
		return ComposeOverview{}, err
	}
	overview := ComposeOverview{Stacks: stacks}
	cli, err := s.resolveComposeCLI(ctx, sessionID)
	if err != nil {
		overview.Message = err.Error()
		for index := range overview.Stacks {
			markComposeReadOnly(&overview.Stacks[index], overview.Message)
		}
		return overview, nil
	}
	overview.CLI = cli
	overview.Available = true
	backend, err := s.composeCommandBackend(ctx, sessionID)
	if err != nil {
		overview.Available = false
		overview.Message = err.Error()
		for index := range overview.Stacks {
			markComposeReadOnly(&overview.Stacks[index], overview.Message)
		}
		return overview, nil
	}
	for index := range overview.Stacks {
		stack := &overview.Stacks[index]
		if stack.ReadOnly {
			continue
		}
		status, probeErr := backend.CheckComposeFiles(ctx, *stack)
		if probeErr != nil {
			markComposeReadOnly(stack, probeErr.Error())
			continue
		}
		stack.EnvExists = status.EnvExists
		if len(status.Missing) > 0 {
			markComposeReadOnly(stack, "Compose 文件或工作目录不存在: "+strings.Join(status.Missing, ", "))
		}
	}
	return overview, nil
}

func (s *Service) discoverComposeStacks(ctx context.Context, sessionID string) ([]ComposeStack, error) {
	return readWithFallback(ctx, s.provider, sessionID, func(backend Backend) ([]ComposeStack, error) {
		containers, err := listContainerDTOs(ctx, backend)
		if err != nil {
			return nil, err
		}
		groups := make(map[string][]ContainerSummary)
		for _, item := range containers {
			if item.ComposeProject != nil && strings.TrimSpace(*item.ComposeProject) != "" {
				groups[*item.ComposeProject] = append(groups[*item.ComposeProject], item)
			}
		}
		projects := make([]string, 0, len(groups))
		for project := range groups {
			projects = append(projects, project)
		}
		sort.Strings(projects)
		stacks := make([]ComposeStack, 0, len(projects))
		for _, project := range projects {
			items := groups[project]
			stack := ComposeStack{Project: project, ConfigFiles: []string{}, Services: composeServices(items)}
			raw, inspectErr := backend.InspectContainer(ctx, items[0].ID)
			if inspectErr != nil {
				markComposeReadOnly(&stack, inspectErr.Error())
				stacks = append(stacks, stack)
				continue
			}
			labels, decodeErr := decodeComposeLabels(raw)
			if decodeErr != nil {
				markComposeReadOnly(&stack, decodeErr.Error())
				stacks = append(stacks, stack)
				continue
			}
			stack = composeStackFromLabels(project, labels)
			stack.Services = composeServices(items)
			stacks = append(stacks, stack)
		}
		return stacks, nil
	})
}

func listContainerDTOs(ctx context.Context, backend Backend) ([]ContainerSummary, error) {
	if lister, ok := backend.(ContainerDTOLister); ok {
		return lister.ListContainerDTOs(ctx, true)
	}
	items, err := backend.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	return containerDTOs(items), nil
}

func composeServices(items []ContainerSummary) []string {
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.ComposeService != nil && *item.ComposeService != "" {
			seen[*item.ComposeService] = struct{}{}
		}
	}
	services := make([]string, 0, len(seen))
	for service := range seen {
		services = append(services, service)
	}
	sort.Strings(services)
	return services
}

func decodeComposeLabels(raw json.RawMessage) (map[string]string, error) {
	var value struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode compose labels: %w", err)
	}
	if value.Config.Labels == nil {
		return map[string]string{}, nil
	}
	return value.Config.Labels, nil
}

func composeStackFromLabels(project string, labels map[string]string) ComposeStack {
	stack := ComposeStack{
		Project: project, WorkingDir: strings.TrimSpace(labels[composeWorkingDirLabel]),
		ConfigFiles: []string{}, Services: []string{},
	}
	for _, file := range strings.Split(labels[composeConfigFilesLabel], ",") {
		file = strings.TrimSpace(file)
		if file != "" {
			stack.ConfigFiles = append(stack.ConfigFiles, resolveComposePath(stack.WorkingDir, file))
		}
	}
	if stack.WorkingDir == "" {
		markComposeReadOnly(&stack, "容器缺少 Compose working_dir 标签")
	} else if len(stack.ConfigFiles) == 0 {
		markComposeReadOnly(&stack, "容器缺少 Compose config_files 标签")
	}
	if stack.WorkingDir != "" {
		stack.EnvPath = joinComposePath(stack.WorkingDir, ".env")
	}
	return stack
}

func resolveComposePath(workingDir, file string) string {
	if path.IsAbs(file) || isWindowsAbsolute(file) || workingDir == "" {
		return file
	}
	return joinComposePath(workingDir, file)
}

func joinComposePath(directory, file string) string {
	if strings.Contains(directory, `\`) && !strings.Contains(directory, "/") {
		return strings.TrimRight(directory, `\`) + `\` + file
	}
	return path.Join(directory, file)
}

func isWindowsAbsolute(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/') || strings.HasPrefix(value, `\\`)
}

func markComposeReadOnly(stack *ComposeStack, reason string) {
	stack.ReadOnly = true
	if stack.Reason == "" {
		stack.Reason = reason
	}
}

func (s *Service) resolveComposeCLI(ctx context.Context, sessionID string) (ComposeCLI, error) {
	s.composeMu.Lock()
	if s.composeProbes == nil {
		s.composeProbes = make(map[string]*composeProbe)
	}
	probe := s.composeProbes[sessionID]
	if probe == nil {
		probe = &composeProbe{ready: make(chan struct{})}
		s.composeProbes[sessionID] = probe
		s.composeMu.Unlock()
		probe.cli, probe.err = s.probeComposeCLI(ctx, sessionID)
		close(probe.ready)
		return probe.cli, probe.err
	}
	s.composeMu.Unlock()
	select {
	case <-probe.ready:
		return probe.cli, probe.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *Service) probeComposeCLI(ctx context.Context, sessionID string) (ComposeCLI, error) {
	backend, err := s.composeCommandBackend(ctx, sessionID)
	if err != nil {
		return "", err
	}
	pluginErr := backend.ProbeCompose(ctx, ComposeCLIPlugin)
	if pluginErr == nil {
		return ComposeCLIPlugin, nil
	}
	legacyErr := backend.ProbeCompose(ctx, ComposeCLILegacy)
	if legacyErr == nil {
		return ComposeCLILegacy, nil
	}
	return "", fmt.Errorf("%w: docker compose 与 docker-compose 均不可用: %w", ErrUnsupported, errors.Join(pluginErr, legacyErr))
}

func (s *Service) composeCommandBackend(ctx context.Context, sessionID string) (ComposeCommandBackend, error) {
	backend, err := s.provider.Command(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	command, ok := backend.(ComposeCommandBackend)
	if !ok {
		return nil, fmt.Errorf("%w: compose command backend", ErrUnsupported)
	}
	return command, nil
}

func (s *Service) ComposeStack(ctx context.Context, sessionID, project string) (ComposeStack, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return ComposeStack{}, errors.New("compose project is required")
	}
	overview, err := s.ComposeStacks(ctx, sessionID)
	if err != nil {
		return ComposeStack{}, err
	}
	for _, stack := range overview.Stacks {
		if stack.Project != project {
			continue
		}
		if stack.ReadOnly {
			return ComposeStack{}, fmt.Errorf("compose stack %s is read-only: %s", project, stack.Reason)
		}
		return stack, nil
	}
	return ComposeStack{}, fmt.Errorf("compose stack %s not found", project)
}

func (s *Service) ComposeAction(ctx context.Context, request ComposeActionRequest) (string, error) {
	switch request.Action {
	case ComposeActionUp, ComposeActionDown, ComposeActionRestart, ComposeActionPull:
	default:
		return "", fmt.Errorf("%w: compose %s", ErrInvalidAction, request.Action)
	}
	timeout := s.config.ActionTimeout
	if request.Action == ComposeActionPull {
		timeout = s.config.PullTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stack, err := s.ComposeStack(ctx, request.SessionID, request.Project)
	if err != nil {
		return "", err
	}
	cli, err := s.resolveComposeCLI(ctx, request.SessionID)
	if err != nil {
		return "", err
	}
	backend, err := s.composeCommandBackend(ctx, request.SessionID)
	if err != nil {
		return "", err
	}
	started := s.config.Now()
	output, err := backend.RunCompose(ctx, cli, stack, request.Action)
	if err != nil {
		return output, err
	}
	s.recordAudit(ctx, AuditEvent{
		SessionID: request.SessionID, AssetID: request.AssetID, Source: auditSource(request.Source),
		Kind: "docker_compose_action", Payload: map[string]any{"project": stack.Project, "action": string(request.Action)},
		ExitCode: 0, Duration: s.config.Now().Sub(started),
	})
	return output, nil
}

func (s *Service) AttachCompose(ctx context.Context, request ComposeAttachRequest) (string, error) {
	if request.Sink == nil {
		return "", errors.New("compose output sink is required")
	}
	if request.Action != ComposeActionLogs && request.Action != ComposeActionUp {
		return "", fmt.Errorf("%w: streaming compose %s", ErrInvalidAction, request.Action)
	}
	result := &composeExecutionResult{}
	var auditOnce sync.Once
	started := s.config.Now()
	record := func(exit StreamExit) {
		if request.Action != ComposeActionUp {
			return
		}
		auditOnce.Do(func() {
			exitCode, exitErr := result.snapshot(exit)
			if exitErr != nil && exitCode == 0 {
				exitCode = -1
			}
			s.recordAudit(context.WithoutCancel(ctx), AuditEvent{
				SessionID: request.SessionID, AssetID: request.AssetID, Source: auditSource(request.Source),
				Kind:     "docker_compose_redeploy",
				Payload:  map[string]any{"project": request.Project, "action": string(request.Action), "contentHash": request.ContentHash},
				ExitCode: exitCode, Duration: s.config.Now().Sub(started),
			})
		})
	}
	attached := false
	defer func() {
		if !attached {
			record(StreamExit{ExitCode: -1, Err: errors.New("redeploy stream setup failed")})
		}
	}()
	tail := 200
	if request.Tail != nil {
		tail = *request.Tail
		if tail < 0 || tail > 5000 {
			return "", errors.New("compose logs tail must be between 0 and 5000")
		}
	}
	token := s.streams.token(request.SessionID)
	setupCtx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	stack, err := s.ComposeStack(setupCtx, request.SessionID, request.Project)
	if err != nil {
		return "", err
	}
	cli, err := s.resolveComposeCLI(setupCtx, request.SessionID)
	if err != nil {
		return "", err
	}
	backend, err := s.composeCommandBackend(setupCtx, request.SessionID)
	if err != nil {
		return "", err
	}
	streamCtx, streamCancel := context.WithCancel(context.Background())
	stopSetupCancel := context.AfterFunc(setupCtx, streamCancel)
	logStream, err := backend.OpenCompose(streamCtx, cli, stack, request.Action, tail)
	if err != nil {
		streamCancel()
		return "", err
	}
	if !stopSetupCancel() && setupCtx.Err() != nil {
		_ = logStream.Reader.Close()
		return "", setupCtx.Err()
	}
	managed := newManagedStream(s.streams, request.SessionID, streamLogs, request.Sink, streamCtx, streamCancel)
	managed.reader = logStream.Reader
	if request.Action == ComposeActionUp {
		managed.reader = newComposeResultReader(logStream.Reader, streamCtx, result)
	}
	if err := s.streams.add(managed, token); err != nil {
		_ = managed.close(err)
		return "", err
	}
	if request.Action == ComposeActionUp {
		go func() {
			exit := <-managed.done
			record(exit)
		}()
	}
	attached = true
	go managed.run()
	return managed.id, nil
}

type composeExitError struct {
	code int
}

func (e *composeExitError) Error() string {
	return fmt.Sprintf("compose exited with code %d", e.code)
}

type composeExecutionResult struct {
	mu        sync.Mutex
	exitCode  int
	err       error
	completed bool
}

func (r *composeExecutionResult) complete(exitCode int, err error) {
	r.mu.Lock()
	r.exitCode = exitCode
	r.err = err
	r.completed = true
	r.mu.Unlock()
}

func (r *composeExecutionResult) snapshot(fallback StreamExit) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.completed {
		return r.exitCode, r.err
	}
	if fallback.Err != nil {
		return -1, fallback.Err
	}
	return fallback.ExitCode, nil
}

type composeResultReader struct {
	reader io.ReadCloser
	stream CommandStream
	ctx    context.Context
	result *composeExecutionResult
	once   sync.Once
}

func newComposeResultReader(reader io.ReadCloser, ctx context.Context, result *composeExecutionResult) *composeResultReader {
	wrapped := &composeResultReader{reader: reader, ctx: ctx, result: result}
	if merged, ok := reader.(*mergedCommandReader); ok {
		wrapped.stream = merged.stream
	}
	return wrapped
}

func (r *composeResultReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if !errors.Is(err, io.EOF) {
		return count, err
	}
	r.once.Do(func() {
		if r.stream == nil {
			r.result.complete(0, nil)
			return
		}
		exitCode, waitErr := r.stream.Wait(r.ctx)
		r.result.complete(exitCode, waitErr)
	})
	r.result.mu.Lock()
	exitCode, waitErr := r.result.exitCode, r.result.err
	r.result.mu.Unlock()
	if waitErr != nil {
		return count, waitErr
	}
	if exitCode != 0 {
		return count, &composeExitError{code: exitCode}
	}
	return count, io.EOF
}

func (r *composeResultReader) Close() error {
	return r.reader.Close()
}

func (s *Service) clearComposeProbe(sessionID string) {
	s.composeMu.Lock()
	delete(s.composeProbes, sessionID)
	s.composeMu.Unlock()
}

func (s *Service) InvalidatePrunePreview(sessionID, token string) {
	s.pruneMu.Lock()
	if preview, ok := s.prunePreviews[token]; ok && preview.sessionID == sessionID {
		delete(s.prunePreviews, token)
	}
	s.pruneMu.Unlock()
}
