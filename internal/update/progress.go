package update

// Progress has the same shape as the fs://progress payload, plus a phase distinguishing the download and install stages.
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
