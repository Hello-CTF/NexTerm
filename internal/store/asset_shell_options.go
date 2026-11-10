package store

import (
	"encoding/json"
	"fmt"
	"unicode"
)

// AssetOptionShellArgs holds the arguments a local asset's shell starts with, as a JSON array of strings.
const AssetOptionShellArgs = "shellArgs"

const (
	maxShellArgs      = 32
	maxShellArgLength = 1024
)

// ValidateShellArgs checks the start arguments of a local asset's shell: at most 32 arguments of at most 1024
// bytes each, without control characters.
func ValidateShellArgs(args []string) error {
	if len(args) > maxShellArgs {
		return fmt.Errorf("Shell 启动参数最多 %d 个", maxShellArgs)
	}
	for _, arg := range args {
		if len(arg) > maxShellArgLength {
			return fmt.Errorf("单个 Shell 启动参数不能超过 %d 字节", maxShellArgLength)
		}
		for _, r := range arg {
			if unicode.IsControl(r) {
				return errString("Shell 启动参数不能包含控制字符")
			}
		}
	}
	return nil
}

// validateAssetShellOptions checks the shellArgs option of a user-written asset. Payloads that are not JSON
// objects are left to the existing option handling, and a missing or null value means no arguments.
func validateAssetShellOptions(raw string) error {
	var options map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil
	}
	encoded, exists := options[AssetOptionShellArgs]
	if !exists || string(encoded) == "null" {
		return nil
	}
	var args []string
	if err := json.Unmarshal(encoded, &args); err != nil {
		return badParam(errString(AssetOptionShellArgs + " 必须是字符串数组"))
	}
	if err := ValidateShellArgs(args); err != nil {
		return badParam(err)
	}
	return nil
}
