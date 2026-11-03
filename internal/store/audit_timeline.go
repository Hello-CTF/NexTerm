package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
)

const (
	timelineStreamAudit   = "audit"
	timelineStreamCommand = "command"
	timelineStreamDevice  = "device"

	timelineDefaultLimit = 100
	timelineMaxLimit     = 500
)

// Streams tie-break at equal timestamps by rank: audit, then command, then device.
func timelineRank(stream string) int {
	switch stream {
	case timelineStreamAudit:
		return 3
	case timelineStreamCommand:
		return 2
	case timelineStreamDevice:
		return 1
	}
	return 0
}

var timelinePresenceKinds = []string{"device_online", "device_offline", "device_failover"}

type AuditTimelineCursor struct {
	TS     int64
	Stream string
	ID     int64
}

type AuditTimelineQuery struct {
	AssetID *string
	Origins []string // subset of "user", "ai", "device"; empty means all
	From    *int64   // inclusive, ms
	To      *int64   // exclusive, ms
	Limit   int64    // default 100, clamped to [1, 500]
	Cursor  *AuditTimelineCursor
}

type AuditTimelineItem struct {
	Stream  string
	Origin  string
	Audit   *AuditRow
	Command *CommandLogRow
	TS      int64
	ID      int64
}

func timelineAssetCandidates(assetID string) []string {
	bare := strings.TrimPrefix(assetID, "device/")
	candidates := []string{assetID}
	for _, candidate := range []string{bare, "device/" + bare} {
		seen := false
		for _, existing := range candidates {
			seen = seen || existing == candidate
		}
		if !seen {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

// timelineCursorMode reports how a stream of the given rank compares with the cursor:
// strictly older rows only, rows at the same timestamp included, or same-rank key order.
func timelineCursorMode(rank int, cursor *AuditTimelineCursor) string {
	switch cr := timelineRank(cursor.Stream); {
	case rank < cr:
		return "le"
	case rank == cr:
		return "key"
	}
	return "lt"
}

// AuditTimeline merges audit, command and device presence rows into one newest-first
// sequence ordered by (ts, stream rank, id). Each stream is paged with its own keyset
// query, so no table is read beyond limit+1 rows.
func (s *Store) AuditTimeline(ctx context.Context, q AuditTimelineQuery) ([]AuditTimelineItem, *AuditTimelineCursor, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = timelineDefaultLimit
	}
	if limit > timelineMaxLimit {
		limit = timelineMaxLimit
	}
	wantUser, wantAI, wantDevice := len(q.Origins) == 0, len(q.Origins) == 0, len(q.Origins) == 0
	for _, origin := range q.Origins {
		switch origin {
		case "user":
			wantUser = true
		case "ai":
			wantAI = true
		case "device":
			wantDevice = true
		default:
			return nil, nil, badParam(fmt.Errorf("unknown audit timeline origin %q", origin))
		}
	}
	if q.Cursor != nil && timelineRank(q.Cursor.Stream) == 0 {
		return nil, nil, badParam(fmt.Errorf("invalid audit timeline cursor"))
	}
	var candidates []string
	if q.AssetID != nil {
		candidates = timelineAssetCandidates(*q.AssetID)
	}
	fetch := int(limit) + 1
	var merged []AuditTimelineItem

	if wantUser || wantAI {
		rows, err := s.timelineAuditRows(ctx, q, candidates, fetch, false, wantUser, wantAI)
		if err != nil {
			return nil, nil, err
		}
		merged = append(merged, rows...)
	}
	if wantUser {
		rows, err := s.timelineCommandRows(ctx, q, candidates, fetch)
		if err != nil {
			return nil, nil, err
		}
		merged = append(merged, rows...)
	}
	if wantDevice {
		rows, err := s.timelineAuditRows(ctx, q, candidates, fetch, true, false, false)
		if err != nil {
			return nil, nil, err
		}
		merged = append(merged, rows...)
	}

	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.TS != b.TS {
			return a.TS > b.TS
		}
		if ra, rb := timelineRank(a.Stream), timelineRank(b.Stream); ra != rb {
			return ra > rb
		}
		return a.ID > b.ID
	})
	var next *AuditTimelineCursor
	if int64(len(merged)) > limit {
		merged = merged[:limit]
		last := merged[len(merged)-1]
		next = &AuditTimelineCursor{TS: last.TS, Stream: last.Stream, ID: last.ID}
	}
	if merged == nil {
		merged = []AuditTimelineItem{}
	}
	return merged, next, nil
}

func (s *Store) timelineAuditRows(ctx context.Context, q AuditTimelineQuery, candidates []string, fetch int, device, wantUser, wantAI bool) ([]AuditTimelineItem, error) {
	audit := query.Use(s.db).AuditRow
	stream, rank, origin := timelineStreamAudit, timelineRank(timelineStreamAudit), "user"
	dao := audit.WithContext(ctx).Where(ownerConditions(ctx, audit.TableName())...)
	if device {
		stream, rank, origin = timelineStreamDevice, timelineRank(timelineStreamDevice), "device"
		dao = dao.Where(audit.Source.Eq("fleet"), audit.Kind.In(timelinePresenceKinds...))
	} else {
		dao = dao.Where(audit.WithContext(ctx).Where(audit.Source.Neq("fleet")).Or(audit.Kind.NotIn(timelinePresenceKinds...)))
		switch {
		case wantUser && !wantAI:
			dao = dao.Where(audit.Source.Neq("ai"))
		case wantAI && !wantUser:
			dao = dao.Where(audit.Source.Eq("ai"))
		}
	}
	if candidates != nil {
		dao = dao.Where(audit.AssetID.In(candidates...))
	}
	if q.From != nil {
		dao = dao.Where(audit.TS.Gte(*q.From))
	}
	if q.To != nil {
		dao = dao.Where(audit.TS.Lt(*q.To))
	}
	if q.Cursor != nil {
		c := q.Cursor
		switch timelineCursorMode(rank, c) {
		case "le":
			dao = dao.Where(audit.TS.Lte(c.TS))
		case "lt":
			dao = dao.Where(audit.TS.Lt(c.TS))
		default:
			dao = dao.Where(audit.WithContext(ctx).Where(audit.TS.Lt(c.TS)).Or(audit.TS.Eq(c.TS), audit.ID.Lt(c.ID)))
		}
	}
	var rows []AuditRow
	if err := dao.Order(audit.TS.Desc(), audit.ID.Desc()).Limit(fetch).Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	items := make([]AuditTimelineItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		itemOrigin := origin
		if !device && row.Source == "ai" {
			itemOrigin = "ai"
		}
		items = append(items, AuditTimelineItem{Stream: stream, Origin: itemOrigin, Audit: row, TS: row.TS, ID: row.ID})
	}
	return items, nil
}

func (s *Store) timelineCommandRows(ctx context.Context, q AuditTimelineQuery, candidates []string, fetch int) ([]AuditTimelineItem, error) {
	command := query.Use(s.db).CommandLog
	dao := command.WithContext(ctx)
	if owner := ipc.OwnerID(ctx); owner != "" {
		dao = dao.Where(command.UserID.Eq(owner))
	}
	if candidates != nil {
		dao = dao.Where(command.AssetID.In(candidates...))
	}
	if q.From != nil {
		dao = dao.Where(command.StartedAt.Gte(*q.From))
	}
	if q.To != nil {
		dao = dao.Where(command.StartedAt.Lt(*q.To))
	}
	if q.Cursor != nil {
		c := q.Cursor
		var cond gen.Condition
		switch timelineCursorMode(timelineRank(timelineStreamCommand), c) {
		case "le":
			cond = command.StartedAt.Lte(c.TS)
		case "lt":
			cond = command.StartedAt.Lt(c.TS)
		default:
			cond = command.WithContext(ctx).Where(command.StartedAt.Lt(c.TS)).Or(command.StartedAt.Eq(c.TS), command.ID.Lt(c.ID))
		}
		dao = dao.Where(cond)
	}
	var rows []CommandLogRow
	if err := dao.Order(command.StartedAt.Desc(), command.ID.Desc()).Limit(fetch).Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	items := make([]AuditTimelineItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		items = append(items, AuditTimelineItem{Stream: timelineStreamCommand, Origin: "user", Command: row, TS: row.StartedAt, ID: row.ID})
	}
	return items, nil
}
