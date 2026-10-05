package sync

import "github.com/ProbiusOfficial/NexTerm/internal/store"

const ProtocolVersion = 1

const (
	TokenHeader       = "X-NexTerm-Sync-Token"
	GatewayAuthHeader = "X-NexTerm-Gateway-Auth"
)

const (
	TokenKindServer = "server"
	TokenKindBox    = "box"
)

type GroupPayload struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Name      string  `json:"name"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

type AssetPayload struct {
	ID          string  `json:"id"`
	GroupID     *string `json:"groupId"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Host        *string `json:"host"`
	Port        *int32  `json:"port"`
	Username    *string `json:"username"`
	AuthKind    *string `json:"authKind"`
	KeyPath     *string `json:"keyPath"`
	CredID      *string `json:"credId"`
	OptionsJSON string  `json:"optionsJson"`
	Tags        string  `json:"tags"`
	Note        string  `json:"note"`
	Sort        int64   `json:"sort"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	DeletedAt   *int64  `json:"deletedAt"`
}

type CredentialPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Secret    string `json:"secret"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

type SnippetPayload struct {
	ID        string  `json:"id"`
	GroupID   *string `json:"groupId"`
	Name      string  `json:"name"`
	Body      string  `json:"body"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

type Bundle struct {
	Protocol       int                         `json:"protocol"`
	Origin         string                      `json:"origin"`
	ExportedAt     int64                       `json:"exportedAt"`
	Groups         []GroupPayload              `json:"groups"`
	Assets         []AssetPayload              `json:"assets"`
	Credentials    []CredentialPayload         `json:"creds"`
	CredTombstones []store.CredentialTombstone `json:"credTombstones,omitempty"`
	Snippets       []SnippetPayload            `json:"snippets,omitempty"`
	Warnings       []string                    `json:"warnings,omitempty"`
}

type ExportRequest struct {
	AssetIDs        []string `json:"assetIds"`
	WithCredentials bool     `json:"withCreds"`
}

type ImportRequest struct {
	Bundle Bundle `json:"bundle"`
	Force  bool   `json:"force"`
}

type SkippedNewerEntry struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	LocalRevision  int64  `json:"localRevision"`
	RemoteRevision int64  `json:"remoteRevision"`
	EqualRevision  bool   `json:"equalRevision"`
}

type ImportReport struct {
	GroupsCreated       int                 `json:"groupsCreated"`
	GroupsUpdated       int                 `json:"groupsUpdated"`
	AssetsCreated       int                 `json:"assetsCreated"`
	AssetsUpdated       int                 `json:"assetsUpdated"`
	CredsCreated        int                 `json:"credsCreated"`
	CredsUpdated        int                 `json:"credsUpdated"`
	CredsDeleted        int                 `json:"credsDeleted"`
	SnippetsCreated     int                 `json:"snippetsCreated"`
	SnippetsUpdated     int                 `json:"snippetsUpdated"`
	SkippedNewer        int                 `json:"skippedNewer"`
	SkippedNewerDetails []SkippedNewerEntry `json:"skippedNewerDetails,omitempty"`
	Refused             int                 `json:"refused"`
	Warnings            []string            `json:"warnings"`
}

type DigestEntry struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Host      *string `json:"host"`
	Username  *string `json:"username"`
	UpdatedAt int64   `json:"updatedAt"`
	DeletedAt *int64  `json:"deletedAt"`
	HasCred   bool    `json:"hasCred"`
	GroupID   *string `json:"groupId"`
}

type Digest struct {
	Origin     string        `json:"origin"`
	Protocol   int           `json:"protocol"`
	AppVersion string        `json:"appVersion"`
	Desktop    bool          `json:"desktop"`
	Assets     []DigestEntry `json:"assets"`
}

type Link struct {
	URL        string `json:"url"`
	TokenKind  string `json:"tokenKind"`
	Token      string `json:"token"`
	Insecure   bool   `json:"insecure"`
	VerifiedAt int64  `json:"verifiedAt"`
	LastError  string `json:"lastError"`
}

func (l Link) IsConfigured() bool {
	return l.URL != "" && l.Token != ""
}

type LinkPatch struct {
	URL       string  `json:"url"`
	TokenKind string  `json:"tokenKind"`
	Token     *string `json:"token"`
	Insecure  *bool   `json:"insecure"`
}

type PushRequest struct {
	AssetIDs        []string `json:"assetIds"`
	WithCredentials bool     `json:"withCreds"`
	Force           bool     `json:"force"`
}

type PullRequest struct {
	AssetIDs        []string `json:"assetIds"`
	WithCredentials bool     `json:"withCreds"`
	Force           bool     `json:"force"`
}
