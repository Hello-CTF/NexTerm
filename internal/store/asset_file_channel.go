package store

import "encoding/json"

// AssetOptionFileChannel chooses how the file manager reaches an SSH host:
// unset or "auto" serves files through the NexTerm helper when it can and
// falls back to SFTP, "sftp" always uses SFTP.
const (
	AssetOptionFileChannel = "fileChannel"
	FileChannelAuto        = "auto"
	FileChannelSFTP        = "sftp"
)

// AssetFileChannelSFTP reports whether stored asset options keep files on SFTP.
func AssetFileChannelSFTP(optionsJSON string) bool {
	var options map[string]json.RawMessage
	if json.Unmarshal([]byte(optionsJSON), &options) != nil {
		return false
	}
	var value string
	if json.Unmarshal(options[AssetOptionFileChannel], &value) != nil {
		return false
	}
	return value == FileChannelSFTP
}

func validateAssetFileChannel(raw string) error {
	var options map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil
	}
	encoded, exists := options[AssetOptionFileChannel]
	if !exists || string(encoded) == "null" {
		return nil
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil || (value != "" && value != FileChannelAuto && value != FileChannelSFTP) {
		return badParam(errString(AssetOptionFileChannel + " 必须是 auto 或 sftp"))
	}
	return nil
}
