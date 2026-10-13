package model

var AllModels = []any{
	AssetGroupRow{}, AssetRow{}, CredentialRow{}, Setting{}, AuditRow{}, ConversationRow{}, MessageRow{}, SnippetRow{}, KnownHostRow{}, RecordingRow{}, OutcomeRecord{}, CronJob{}, RunRow{}, RunEventRow{}, HitlRunRow{}, CheckpointRow{},
	TranscriptRow{}, TranscriptChunkRow{}, DurableTranscriptOffset{}, DurableTabTitle{}, CredentialTombstone{}, SyncTombstone{}, SyncState{}, MemoryEntry{}, MemorySettings{},
	AppUser{}, UserDEK{}, UserDevice{}, UserSession{}, SyncCredential{}, UserSetting{}, DeviceEnrollCode{}, UserSyncObject{}, UserSyncHead{}, DeviceAgent{}, DeviceMetrics{}, DeviceMetricsHourly{}, UserTOTP{}, UserTOTPRecoveryCode{},
	ShareLink{}, SharePreviewLink{}, HostShare{}, CommandLog{}, DeviceShareLink{},
}
