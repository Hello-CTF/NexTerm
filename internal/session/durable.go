package session

type durableVersionFloor interface {
	DurableVersions() (eventVersion, gridRevision uint64, err error)
	PersistDurableVersions(eventVersion, gridRevision uint64) error
}

type durableGridSource interface {
	DurableGrid() (cols, rows uint32, ok bool)
}

type DurableTranscriptOffsetStore interface {
	DurableTranscriptCatchUpBytes(tabID string) int64
	PersistDurableTranscriptOffset(tabID string, offset int64)
	DeleteDurableTranscriptOffset(tabID string)
}

type durableTranscriptOffsetSource = DurableTranscriptOffsetStore
