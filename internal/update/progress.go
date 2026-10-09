package update

// Progress 与 fs://progress 载荷同形, 额外带 phase 区分下载与安装阶段。
type Progress struct {
	TaskID      string `json:"taskId"`
	Phase       string `json:"phase,omitempty"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
	Done        bool   `json:"done"`
	Error       string `json:"error,omitempty"`
}

const (
	PhaseDownload = "download"
	PhaseInstall  = "install"
)
