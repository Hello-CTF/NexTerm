package store

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Asset option keys for files pasted or dropped into a terminal. pasteUploadDir is the
// directory as the file channel (SFTP) sees it; pasteTerminalDir is the same directory as
// the shell sees it, for chrooted SFTP accounts whose view differs from the shell's.
const (
	AssetOptionPasteUploadDir   = "pasteUploadDir"
	AssetOptionPasteTerminalDir = "pasteTerminalDir"
	// AssetOptionPasteNoRandomDir uploads pasted files straight into the upload directory instead of a
	// per-paste private directory.
	AssetOptionPasteNoRandomDir = "pasteNoRandomDir"
	// AssetOptionBackupBeforeOverwrite keeps a .nexterm-bak copy on the host when an AI file tool or a Compose
	// redeploy overwrites an existing file. Off unless the asset enables it.
	AssetOptionBackupBeforeOverwrite = "backupBeforeOverwrite"
)

// AssetBackupBeforeOverwrite reports whether stored asset options enable the pre-overwrite backup. Anything other
// than a literal true (missing, null, wrong type, invalid JSON) means off.
func AssetBackupBeforeOverwrite(optionsJSON string) bool {
	var options map[string]json.RawMessage
	if json.Unmarshal([]byte(optionsJSON), &options) != nil {
		return false
	}
	return string(options[AssetOptionBackupBeforeOverwrite]) == "true"
}

// normalizePasteDir validates an absolute POSIX directory and returns it without
// duplicate or trailing slashes and "." segments. Empty input means "not configured".
func normalizePasteDir(raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return "", errString("路径不能包含控制字符")
		}
	}
	value := strings.Trim(raw, " ")
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") {
		return "", errString("路径必须是以 / 开头的绝对路径")
	}
	segments := make([]string, 0, strings.Count(value, "/"))
	for _, segment := range strings.Split(value, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			return "", errString("路径不能包含 .. 段")
		}
		segments = append(segments, segment)
	}
	return "/" + strings.Join(segments, "/"), nil
}

// validateAssetPasteOptions checks the paste upload directory options of a user-written
// asset. Payloads that are not JSON objects are left to the existing option handling.
func validateAssetPasteOptions(raw string) error {
	var options map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil
	}
	for _, key := range []string{AssetOptionPasteNoRandomDir, AssetOptionBackupBeforeOverwrite} {
		if encoded, exists := options[key]; exists && string(encoded) != "null" {
			var flag bool
			if err := json.Unmarshal(encoded, &flag); err != nil {
				return badParam(errString(key + " 必须是布尔值"))
			}
		}
	}
	values := map[string]string{}
	for _, key := range []string{AssetOptionPasteUploadDir, AssetOptionPasteTerminalDir} {
		encoded, exists := options[key]
		if !exists || string(encoded) == "null" {
			continue
		}
		var value string
		if err := json.Unmarshal(encoded, &value); err != nil {
			return badParam(errString(key + " 必须是字符串"))
		}
		normalized, err := normalizePasteDir(value)
		if err != nil {
			return badParam(errString(pasteOptionLabel(key) + err.Error()))
		}
		values[key] = normalized
	}
	if values[AssetOptionPasteTerminalDir] != "" && values[AssetOptionPasteUploadDir] == "" {
		return badParam(errString("填写终端中的对应路径时，必须同时填写粘贴上传目录"))
	}
	return nil
}

func pasteOptionLabel(key string) string {
	if key == AssetOptionPasteTerminalDir {
		return "终端中的对应路径无效："
	}
	return "粘贴上传目录无效："
}
