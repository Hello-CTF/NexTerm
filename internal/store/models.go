package store

import "github.com/Hello-CTF/NexTerm/internal/store/model"

const (
	BuiltinLocalAssetID   = "01J0NEXTERMLOCALDEVICE0001"
	BuiltinLocalAssetName = "当前设备"
	LayoutKey             = "layout.workspace"
	// DeviceKindLocalHelper marks a desktop that exposes its local helper
	// sessions to its own account through the device bridge. Such a device is
	// never shared with another account.
	DeviceKindLocalHelper = "local-helper"
)

type AssetGroupRow = model.AssetGroupRow
type AssetRow = model.AssetRow
type CredentialRow = model.CredentialRow
type CredentialMeta = model.CredentialMeta
type Setting = model.Setting

type AuditRow = model.AuditRow
type ConversationRow = model.ConversationRow
type MessageRow = model.MessageRow
type SnippetRow = model.SnippetRow
type KnownHostRow = model.KnownHostRow
type RecordingRow = model.RecordingRow
type OutcomeRecord = model.OutcomeRecord
type CronJob = model.CronJob
type SyncTombstone = model.SyncTombstone
type SyncState = model.SyncState
type AppUser = model.AppUser
type UserDEK = model.UserDEK
type UserDevice = model.UserDevice
type UserSession = model.UserSession
type SyncCredential = model.SyncCredential
type UserSetting = model.UserSetting
type DeviceEnrollCode = model.DeviceEnrollCode
type UserSyncObject = model.UserSyncObject
type UserSyncHead = model.UserSyncHead
type DeviceAgent = model.DeviceAgent
type DeviceMetrics = model.DeviceMetrics
type DeviceMetricsHourly = model.DeviceMetricsHourly
type UserTOTP = model.UserTOTP
type UserTOTPRecoveryCode = model.UserTOTPRecoveryCode
type SharePreviewLink = model.SharePreviewLink
type HostShare = model.HostShare
type CommandLog = model.CommandLog
type ExecBatchRow = model.ExecBatchRow
type ExecBatchResultRow = model.ExecBatchResultRow

type AssetRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type GroupInput struct {
	ParentID *string
	Name     string
	Sort     int64
}

type AssetInput struct {
	GroupID     *string
	Kind        string
	Name        string
	Host        *string
	Port        *int32
	Username    *string
	AuthKind    *string
	KeyPath     *string
	CredID      *string
	OptionsJSON string
	Tags        string
	Note        string
	Sort        int64
}

type Optional[T any] struct {
	Set   bool
	Value *T
}

func Value[T any](value T) Optional[T] {
	return Optional[T]{Set: true, Value: &value}
}

func Null[T any]() Optional[T] {
	return Optional[T]{Set: true}
}

type GroupPatch struct {
	Name     *string
	ParentID Optional[string]
	Sort     *int64
}

type AssetPatch struct {
	GroupID     Optional[string]
	Name        *string
	Host        Optional[string]
	Port        Optional[int32]
	Username    Optional[string]
	AuthKind    Optional[string]
	KeyPath     Optional[string]
	CredID      Optional[string]
	OptionsJSON *string
	Tags        *string
	Note        *string
	Sort        *int64
}

type CredentialInput struct {
	ID      string
	Name    string
	Kind    string
	Nonce   []byte
	Blob    []byte
	KEKHint string
}

type AuditQuery struct {
	SessionID *string
	AssetID   *string
	Source    *string
	Kind      *string
	Limit     int64
	Offset    int64
}

type AuditInput struct {
	SessionID  *string
	AssetID    *string
	Source     string
	Kind       string
	Payload    any
	ExitCode   *int32
	DurationMS *int64
}
