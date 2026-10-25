package model

var AllModels = []any{
	AssetGroupRow{}, AssetRow{}, CredentialRow{}, Setting{}, AuditRow{}, ConversationRow{}, MessageRow{}, SnippetRow{}, KnownHostRow{}, RecordingRow{}, OutcomeRecord{}, CronJob{}, RunRow{}, RunEventRow{}, HitlRunRow{}, CheckpointRow{},
	TranscriptRow{}, TranscriptChunkRow{}, TranscriptChunkTS{}, TranscriptTSIndex{}, DurableTranscriptOffset{}, DurableTabTitle{}, CredentialTombstone{}, SyncTombstone{}, SyncState{}, MemoryEntry{}, MemorySettings{},
	AgentWatchRow{}, AgentWatchOutboxRow{}, AgentWatchTargetRow{},
	AppUser{}, UserDEK{}, UserDevice{}, UserSession{}, SyncCredential{}, UserSetting{}, DeviceEnrollCode{}, UserSyncObject{}, UserSyncHead{}, DeviceAgent{}, DeviceMetrics{}, DeviceMetricsHourly{}, UserTOTP{}, UserTOTPRecoveryCode{},
	SharePreviewLink{}, HostShare{}, CommandLog{}, ForwardRuleRow{}, ExecBatchRow{}, ExecBatchResultRow{}, ExecTargetSetRow{}, ExecTargetSetItemRow{}, AIOwnerMigration{},
	AgentRosterRow{}, AgentOutcomeRow{}, AgentEvidenceKeyRow{}, AgentEvidenceAnchorRow{},
}
