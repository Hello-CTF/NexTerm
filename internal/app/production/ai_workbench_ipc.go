package production

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	aiConversationPageLimit = 200
	aiConversationIDLimit   = 10000
	aiConversationKeyBytes  = 191

	aiConversationExportMarkdown = "md"
	aiConversationSkipRunning    = "running"
)

// aiConversationFilter selects conversations for the AI workbench and its export; every field is optional.
type aiConversationFilter struct {
	// IDs limits the selection to these conversations.
	IDs []string `json:"ids,omitempty"`
	// AssetID keeps the conversations whose scope names this host; Unlinked keeps those naming none.
	AssetID  string `json:"assetId,omitempty"`
	Unlinked bool   `json:"unlinked,omitempty"`
	// Status is running, interrupted, completed, failed or idle.
	Status string `json:"status,omitempty"`
	// Query matches the title or the saved message text.
	Query string `json:"query,omitempty"`
	// UpdatedFrom (inclusive) and UpdatedBefore (exclusive) bound the last activity in Unix ms; 0 is unbounded.
	UpdatedFrom   int64 `json:"updatedFrom,omitempty"`
	UpdatedBefore int64 `json:"updatedBefore,omitempty"`
}

type aiConversationOverviewRequest struct {
	aiConversationFilter
	Offset int `json:"offset,omitempty"`
	// Limit is the page size, at most 200; 0 selects 50.
	Limit int `json:"limit,omitempty"`
}

type aiConversationSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	AssetID   string `json:"assetId"`
	Status    string `json:"status"`
	RunCount  int64  `json:"runCount"`
	LastRunAt int64  `json:"lastRunAt"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type aiConversationHostCountDTO struct {
	// AssetID is empty for the conversations that name no host.
	AssetID string `json:"assetId"`
	Count   int64  `json:"count"`
}

type aiConversationOverviewResult struct {
	Items []aiConversationSummaryDTO `json:"items"`
	// Total counts every conversation the filter selects, across pages.
	Total int `json:"total"`
	// Hosts counts the conversations per host under every filter except the host one.
	Hosts []aiConversationHostCountDTO `json:"hosts"`
}

type aiConversationApprovalDTO struct {
	RunID  string `json:"runId"`
	CallID string `json:"callId"`
	// Kind is "confirm" or "question".
	Kind     string `json:"kind"`
	Tool     string `json:"tool,omitempty"`
	Rendered string `json:"rendered,omitempty"`
	Risk     string `json:"risk,omitempty"`
	Question string `json:"question,omitempty"`
	// Decision is empty while the request is unresolved.
	Decision    string `json:"decision"`
	RequestedAt int64  `json:"requestedAt"`
	ResolvedAt  *int64 `json:"resolvedAt"`
}

type aiConversationExportRequest struct {
	aiConversationFilter
	// Path is the file to write; it is not needed for a dry run.
	Path string `json:"path,omitempty"`
	// Format is "zip" (default), "tar.gz", or "md" for one conversation as a bare Markdown file.
	Format string `json:"format,omitempty"`
	// IncludeJSON adds each conversation's raw history as JSON next to its Markdown.
	IncludeJSON bool `json:"includeJson,omitempty"`
	// Labels are the Markdown wording in the viewer's language; missing keys use the source wording.
	Labels map[string]string `json:"labels,omitempty"`
	// HostNames names the hosts the viewer knows; hosts missing here are named from the asset list.
	HostNames        map[string]string `json:"hostNames,omitempty"`
	TimeZone         string            `json:"timeZone,omitempty"`
	UTCOffsetMinutes *int              `json:"utcOffsetMinutes,omitempty"`
	DryRun           bool              `json:"dryRun,omitempty"`
}

type aiConversationExportSkipped struct {
	// Running counts conversations whose AI run is still writing to them.
	Running int64 `json:"running"`
}

type aiConversationExportResult struct {
	Path     string                      `json:"path"`
	Matched  int64                       `json:"matched"`
	Exported int64                       `json:"exported"`
	Skipped  aiConversationExportSkipped `json:"skipped"`
	// Files counts the conversation files written, without the manifest.
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

type aiConversationManifestEntry struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	AssetID   string   `json:"assetId"`
	Host      string   `json:"host"`
	Status    string   `json:"status"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
	Files     []string `json:"files"`
}

type aiConversationManifestSkip struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	AssetID string `json:"assetId"`
	Host    string `json:"host"`
	Reason  string `json:"reason"`
}

type aiConversationManifest struct {
	Version       int                           `json:"version"`
	GeneratedAt   int64                         `json:"generatedAt"`
	IncludeJSON   bool                          `json:"includeJson"`
	Filter        aiConversationFilter          `json:"filter"`
	Conversations []aiConversationManifestEntry `json:"conversations"`
	Skipped       []aiConversationManifestSkip  `json:"skipped"`
}

type aiConversationExportPlan struct {
	rows     []store.ConvOverviewRow
	hosts    map[string]string
	manifest aiConversationManifest
	result   aiConversationExportResult
}

type aiWorkbenchCommands struct {
	database *store.Store
	staged   StagedBlobResolver
}

// registerAIWorkbenchCommands registers the read-only AI workbench queries and the conversation export.
func registerAIWorkbenchCommands(dispatcher *ipc.Dispatcher, database *store.Store, staged StagedBlobResolver) error {
	commands := &aiWorkbenchCommands{database: database, staged: staged}
	if err := ipc.Register(dispatcher, "ai_conversation_overview", func(ctx context.Context, _ *ipc.Call, input aiConversationOverviewRequest) (aiConversationOverviewResult, error) {
		return commands.overview(ctx, input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "ai_conversation_approvals", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) ([]aiConversationApprovalDTO, error) {
		return commands.approvals(ctx, input.ConversationID)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "ai_conversation_export", func(ctx context.Context, _ *ipc.Call, input aiConversationExportRequest) (aiConversationExportResult, error) {
		return commands.export(ctx, input)
	})
}

func (f aiConversationFilter) validate() error {
	if len(f.IDs) > aiConversationIDLimit {
		return ipc.BadParam(fmt.Errorf("一次最多选择 %d 个会话", aiConversationIDLimit))
	}
	for _, id := range f.IDs {
		if id == "" || len(id) > aiConversationKeyBytes {
			return ipc.BadParam(errors.New("会话 ID 格式不正确"))
		}
	}
	if len(f.AssetID) > aiConversationKeyBytes {
		return ipc.BadParam(errors.New("主机 ID 格式不正确"))
	}
	if f.AssetID != "" && f.Unlinked {
		return ipc.BadParam(errors.New("不能同时筛选主机和未关联主机"))
	}
	switch f.Status {
	case "", store.ConvStatusRunning, store.ConvStatusInterrupted, store.ConvStatusCompleted, store.ConvStatusFailed, store.ConvStatusIdle:
	default:
		return ipc.BadParam(fmt.Errorf("不支持的会话状态：%s", truncateForError(f.Status)))
	}
	return nil
}

// selectConversations applies the filter; hosts are counted before the host filter so the picker shows every host.
func (c *aiWorkbenchCommands) selectConversations(ctx context.Context, filter aiConversationFilter) ([]store.ConvOverviewRow, []aiConversationHostCountDTO, error) {
	if err := filter.validate(); err != nil {
		return nil, nil, err
	}
	rows, err := c.database.ConvOverview(ctx, store.ConvOverviewFilter{
		IDs: filter.IDs, Query: filter.Query, UpdatedFrom: filter.UpdatedFrom, UpdatedBefore: filter.UpdatedBefore,
	})
	if err != nil {
		return nil, nil, err
	}
	byStatus := store.SelectConvOverview(rows, "", false, filter.Status)
	counts := map[string]int64{}
	order := []string{}
	for _, row := range byStatus {
		if _, seen := counts[row.AssetID]; !seen {
			order = append(order, row.AssetID)
		}
		counts[row.AssetID]++
	}
	hosts := make([]aiConversationHostCountDTO, len(order))
	for index, assetID := range order {
		hosts[index] = aiConversationHostCountDTO{AssetID: assetID, Count: counts[assetID]}
	}
	return store.SelectConvOverview(byStatus, filter.AssetID, filter.Unlinked, ""), hosts, nil
}

func (c *aiWorkbenchCommands) overview(ctx context.Context, input aiConversationOverviewRequest) (aiConversationOverviewResult, error) {
	if input.Offset < 0 || input.Limit < 0 {
		return aiConversationOverviewResult{}, ipc.BadParam(errors.New("分页参数无效"))
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	limit = min(limit, aiConversationPageLimit)
	rows, hosts, err := c.selectConversations(ctx, input.aiConversationFilter)
	if err != nil {
		return aiConversationOverviewResult{}, err
	}
	result := aiConversationOverviewResult{Items: []aiConversationSummaryDTO{}, Total: len(rows), Hosts: hosts}
	if input.Offset >= len(rows) {
		return result, nil
	}
	for _, row := range rows[input.Offset:min(input.Offset+limit, len(rows))] {
		result.Items = append(result.Items, aiConversationSummaryDTO{
			ID: row.ID, Title: row.Title, AssetID: row.AssetID, Status: row.Status, RunCount: row.RunCount,
			LastRunAt: row.LastRunAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return result, nil
}

func (c *aiWorkbenchCommands) approvals(ctx context.Context, conversationID string) ([]aiConversationApprovalDTO, error) {
	if conversationID == "" || len(conversationID) > aiConversationKeyBytes {
		return nil, ipc.BadParam(errors.New("会话 ID 格式不正确"))
	}
	events, err := c.database.ConvInteractionEvents(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	return approvalsFromEvents(events), nil
}

// approvalsFromEvents pairs each confirmation or question with the event that resolved it.
func approvalsFromEvents(events []store.RunEventRow) []aiConversationApprovalDTO {
	result := []aiConversationApprovalDTO{}
	open := map[string]int{}
	for _, event := range events {
		var payload struct {
			ID       string `json:"id"`
			CallID   string `json:"callId"`
			Tool     string `json:"tool"`
			Rendered string `json:"rendered"`
			Risk     string `json:"risk"`
			Question string `json:"question"`
			Decision string `json:"decision"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil {
			continue
		}
		switch event.Type {
		case "confirmRequired", "questionRequired":
			approval := aiConversationApprovalDTO{
				RunID: event.RunID, CallID: payload.ID, Kind: "confirm", Tool: payload.Tool, Rendered: payload.Rendered,
				Risk: payload.Risk, RequestedAt: event.CreatedAt,
			}
			if event.Type == "questionRequired" {
				approval.Kind, approval.Question = "question", payload.Question
			}
			open[event.RunID+"\x00"+payload.ID] = len(result)
			result = append(result, approval)
		case "interactionResolved":
			key := event.RunID + "\x00" + payload.CallID
			index, ok := open[key]
			if !ok {
				continue
			}
			delete(open, key)
			resolvedAt := event.CreatedAt
			result[index].Decision, result[index].ResolvedAt = payload.Decision, &resolvedAt
		}
	}
	return result
}

func (c *aiWorkbenchCommands) export(ctx context.Context, input aiConversationExportRequest) (aiConversationExportResult, error) {
	if input.Format == "" {
		input.Format = transcriptArchiveZip
	}
	switch input.Format {
	case transcriptArchiveZip, transcriptArchiveTarGz, aiConversationExportMarkdown:
	default:
		return aiConversationExportResult{}, ipc.BadParam(fmt.Errorf("不支持的导出格式：%s", truncateForError(input.Format)))
	}
	if input.Format == aiConversationExportMarkdown && (len(input.IDs) != 1 || input.IncludeJSON) {
		return aiConversationExportResult{}, ipc.BadParam(errors.New("Markdown 文件只能导出单个会话，且不含 JSON"))
	}
	if len(input.Labels) > 200 || len(input.HostNames) > aiConversationIDLimit {
		return aiConversationExportResult{}, ipc.BadParam(errors.New("导出参数过多"))
	}
	location, err := viewerLocation(input.TimeZone, input.UTCOffsetMinutes)
	if err != nil {
		return aiConversationExportResult{}, err
	}
	plan, err := c.planExport(ctx, input, location)
	if err != nil {
		return aiConversationExportResult{}, err
	}
	if input.DryRun {
		return plan.result, nil
	}
	if plan.result.Exported == 0 {
		return plan.result, ipc.BadParam(errors.New("所选范围内没有可导出的会话"))
	}
	path, maxBytes, release, err := resolveStagedWriteTarget(ctx, c.staged, input.Path)
	if err != nil {
		return aiConversationExportResult{}, err
	}
	defer release()
	labels := newConversationLabels(input.Labels)
	result := plan.result
	err = atomicfile.Write(path, 0o600, func(out io.Writer) error {
		counted := &countingWriter{out: fslocal.LimitWriter(out, maxBytes)}
		var writeErr error
		if input.Format == aiConversationExportMarkdown {
			writeErr = c.writeConversationMarkdown(ctx, plan.rows[0], plan.hosts[plan.rows[0].AssetID], labels, location, counted)
		} else {
			writeErr = c.writeConversationArchive(ctx, input, plan, labels, location, counted, filepath.Dir(path), maxBytes)
		}
		result.Bytes = counted.n
		return writeErr
	})
	if errors.Is(err, base.ErrTransferTooLarge) {
		return aiConversationExportResult{}, fsIPCError("", err)
	}
	if err != nil {
		return aiConversationExportResult{}, err
	}
	result.Path = path
	return result, nil
}

// planExport selects the conversations and decides each one's files, so a dry run and the export count alike.
func (c *aiWorkbenchCommands) planExport(ctx context.Context, input aiConversationExportRequest, location *time.Location) (aiConversationExportPlan, error) {
	rows, _, err := c.selectConversations(ctx, input.aiConversationFilter)
	if err != nil {
		return aiConversationExportPlan{}, err
	}
	labels := newConversationLabels(input.Labels)
	plan := aiConversationExportPlan{hosts: map[string]string{}, manifest: aiConversationManifest{
		Version: 1, GeneratedAt: time.Now().UnixMilli(), IncludeJSON: input.IncludeJSON, Filter: input.aiConversationFilter,
		Conversations: []aiConversationManifestEntry{}, Skipped: []aiConversationManifestSkip{},
	}}
	plan.result.Matched = int64(len(rows))
	for _, row := range rows {
		host, ok := plan.hosts[row.AssetID]
		if !ok {
			host = c.hostName(ctx, row.AssetID, input.HostNames, labels)
			plan.hosts[row.AssetID] = host
		}
		// A bare Markdown file is an explicit snapshot of one conversation; an archive leaves out conversations
		// an AI run is still writing to.
		if row.Status == store.ConvStatusRunning && input.Format != aiConversationExportMarkdown {
			plan.result.Skipped.Running++
			plan.manifest.Skipped = append(plan.manifest.Skipped, aiConversationManifestSkip{
				ID: row.ID, Title: row.Title, AssetID: row.AssetID, Host: host, Reason: aiConversationSkipRunning,
			})
			continue
		}
		base := conversationExportBaseName(row, location)
		entry := aiConversationManifestEntry{
			ID: row.ID, Title: row.Title, AssetID: row.AssetID, Host: host, Status: row.Status,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		if input.Format == aiConversationExportMarkdown {
			entry.Files = []string{base + ".md"}
		} else {
			dir := transcriptArchiveDir(host)
			entry.Files = []string{dir + "/" + base + ".md"}
			if input.IncludeJSON {
				entry.Files = append(entry.Files, dir+"/"+base+".json")
			}
		}
		plan.rows = append(plan.rows, row)
		plan.manifest.Conversations = append(plan.manifest.Conversations, entry)
		plan.result.Exported++
		plan.result.Files += int64(len(entry.Files))
	}
	return plan, nil
}

func (c *aiWorkbenchCommands) hostName(ctx context.Context, assetID string, known map[string]string, labels conversationLabels) string {
	if assetID == "" {
		return labels.get("unlinked")
	}
	if name := strings.TrimSpace(known[assetID]); name != "" {
		return name
	}
	if !strings.HasPrefix(assetID, "device/") {
		if asset, err := c.database.AssetGet(ctx, assetID); err == nil && strings.TrimSpace(asset.Name) != "" {
			return strings.TrimSpace(asset.Name)
		}
	}
	return assetID
}

// conversationExportBaseName is <YYYY-MM-DD-HHmmss>-<title>-<last 6 id chars> in the viewer's zone, from the
// conversation's creation time.
func conversationExportBaseName(row store.ConvOverviewRow, location *time.Location) string {
	suffix := row.ID
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	title := []rune(strings.Join(strings.Fields(row.Title), " "))
	if len(title) > 60 {
		title = title[:60]
	}
	started := time.UnixMilli(row.CreatedAt).In(location).Format("2006-01-02-150405")
	name := started
	if len(title) > 0 {
		name += "-" + string(title)
	}
	return sanitizeExportName(name + "-" + suffix)
}

func (c *aiWorkbenchCommands) conversationContent(ctx context.Context, row store.ConvOverviewRow) ([]store.MessageRow, []aiConversationApprovalDTO, error) {
	messages, err := c.database.MsgList(ctx, row.ID)
	if err != nil {
		return nil, nil, err
	}
	events, err := c.database.ConvInteractionEvents(ctx, row.ID)
	if err != nil {
		return nil, nil, err
	}
	return messages, approvalsFromEvents(events), nil
}

func (c *aiWorkbenchCommands) writeConversationMarkdown(ctx context.Context, row store.ConvOverviewRow, host string, labels conversationLabels, location *time.Location, out io.Writer) error {
	messages, approvals, err := c.conversationContent(ctx, row)
	if err != nil {
		return err
	}
	return renderConversationMarkdown(out, conversationDocument{row: row, host: host, messages: messages, approvals: approvals}, labels, location)
}

func (c *aiWorkbenchCommands) writeConversationArchive(ctx context.Context, input aiConversationExportRequest, plan aiConversationExportPlan,
	labels conversationLabels, location *time.Location, out io.Writer, tempDir string, maxBytes int64) error {
	var archive transcriptArchiveWriter
	if input.Format == transcriptArchiveTarGz {
		archive = newTarGzArchive(out, tempDir, maxBytes)
	} else {
		archive = &zipArchive{writer: zip.NewWriter(out)}
	}
	manifest, err := json.MarshalIndent(plan.manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := archive.Add(transcriptArchiveManifestName, time.UnixMilli(plan.manifest.GeneratedAt), func(w io.Writer) error {
		_, err := w.Write(append(manifest, '\n'))
		return err
	}); err != nil {
		return err
	}
	for index, row := range plan.rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		messages, approvals, err := c.conversationContent(ctx, row)
		if err != nil {
			return err
		}
		document := conversationDocument{row: row, host: plan.hosts[row.AssetID], messages: messages, approvals: approvals}
		modified := time.UnixMilli(row.UpdatedAt)
		for _, name := range plan.manifest.Conversations[index].Files {
			var add func(io.Writer) error
			if strings.HasSuffix(name, ".json") {
				add = func(w io.Writer) error { return c.writeConversationJSON(ctx, document, w) }
			} else {
				add = func(w io.Writer) error { return renderConversationMarkdown(w, document, labels, location) }
			}
			if err := archive.Add(name, modified, add); err != nil {
				return err
			}
		}
	}
	return archive.Close()
}

type conversationJSONMessage struct {
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	TokensIn  *int64          `json:"tokensIn"`
	TokensOut *int64          `json:"tokensOut"`
	CreatedAt int64           `json:"createdAt"`
}

type conversationJSONRun struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	ProfileID  string `json:"profileId"`
	Turns      int    `json:"turns"`
	TokensIn   int64  `json:"tokensIn"`
	TokensOut  int64  `json:"tokensOut"`
	Error      string `json:"error"`
	CreatedAt  int64  `json:"createdAt"`
	FinishedAt *int64 `json:"finishedAt"`
}

func (c *aiWorkbenchCommands) writeConversationJSON(ctx context.Context, document conversationDocument, out io.Writer) error {
	runs, err := c.database.RunList(ctx, document.row.ID, 0)
	if err != nil {
		return err
	}
	messages := make([]conversationJSONMessage, len(document.messages))
	for index, message := range document.messages {
		messages[index] = conversationJSONMessage{
			ID: message.ID, Role: message.Role, Content: store.ParseJSONOr(message.ContentJSON),
			TokensIn: message.TokensIn, TokensOut: message.TokensOut, CreatedAt: message.CreatedAt,
		}
	}
	runRows := make([]conversationJSONRun, len(runs))
	for index, run := range runs {
		runRows[index] = conversationJSONRun{
			ID: run.ID, Status: run.Status, Source: run.Source, ProfileID: run.ProfileID, Turns: run.Turns,
			TokensIn: run.TokensIn, TokensOut: run.TokensOut, Error: run.Error, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt,
		}
	}
	row := document.row
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(map[string]any{
		"conversation": map[string]any{
			"id": row.ID, "title": row.Title, "scope": store.ParseJSONOr(row.ScopeJSON), "assetId": row.AssetID,
			"host": document.host, "status": row.Status, "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt,
		},
		"messages":     messages,
		"runs":         runRows,
		"interactions": document.approvals,
	})
}
