//go:build unix

package agent

import (
	"os"
	"strconv"
)

// currentIdentity 返回与 internal/supervisor 一致的 unix 用户标识
// (euid 十进制串), 用于计算 hello 上报的 supervisor 状态摘要。
func currentIdentity() (string, error) {
	return strconv.Itoa(os.Geteuid()), nil
}
