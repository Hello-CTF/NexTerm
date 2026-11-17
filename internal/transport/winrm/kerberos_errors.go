package winrm

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jcmturner/gokrb5/v8/iana/errorcode"
	"github.com/jcmturner/gokrb5/v8/krberror"
	"github.com/jcmturner/gokrb5/v8/messages"
)

type kerberosFailure int

const (
	kerberosFailureUnknown kerberosFailure = iota
	kerberosFailureKDCUnreachable
	kerberosFailureKDCNotFound
	kerberosFailureClockSkew
	kerberosFailurePrincipalUnknown
	kerberosFailureSPNUnknown
	kerberosFailureBadPassword
	kerberosFailureAccountRevoked
	kerberosFailurePasswordExpired
	kerberosFailureEncryptionType
	kerberosFailureWrongRealm
	kerberosFailureTicketNotAccepted
	kerberosFailureRejected
	kerberosFailureNoMutualAuth
	kerberosFailureMutualAuth
)

var kerberosFailureMessages = map[kerberosFailure]string{
	kerberosFailureKDCUnreachable:    "无法连接 Kerberos KDC（域控制器）：请检查网络、防火墙（TCP 88）和 KDC 地址",
	kerberosFailureKDCNotFound:       "找不到域的 Kerberos KDC：DNS 中没有 _kerberos._tcp SRV 记录，请检查 DNS 设置或在 Kerberos 设置中填写 KDC 地址",
	kerberosFailureClockSkew:         "Kerberos 时间偏差过大：本机与域控制器或目标主机的时间相差超过 5 分钟，请同步系统时间",
	kerberosFailurePrincipalUnknown:  "Kerberos 找不到该用户：请检查用户名和域（realm）",
	kerberosFailureSPNUnknown:        "域控制器中找不到目标主机的服务主体名称（SPN）：请使用主机在域中的完整域名，或在 Kerberos 设置中指定 SPN",
	kerberosFailureBadPassword:       "Kerberos 预认证失败：用户名或密码错误",
	kerberosFailureAccountRevoked:    "Kerberos 拒绝该账户：账户已禁用、锁定或过期",
	kerberosFailurePasswordExpired:   "Kerberos 拒绝该账户：密码已过期，请先修改密码",
	kerberosFailureEncryptionType:    "域控制器不支持所需的加密类型：NexTerm 的 Kerberos 仅使用 AES，请确认账户和目标主机已启用 AES",
	kerberosFailureWrongRealm:        "Kerberos 域（realm）不正确：请填写域的 DNS 名称，如 CORP.EXAMPLE.COM",
	kerberosFailureTicketNotAccepted: "目标主机无法验证 Kerberos 票据：SPN 可能注册在其他账户上，或主机的计算机账户密码与域不同步",
	kerberosFailureRejected:          "目标主机拒绝了 Kerberos 认证：请确认 WinRM 已启用 Kerberos 认证，且该用户有远程管理权限",
	kerberosFailureNoMutualAuth:      "目标主机没有返回 Kerberos 双向认证应答，无法建立 WinRM 消息加密",
	kerberosFailureMutualAuth:        "目标主机的 Kerberos 双向认证应答无效",
}

// kerberosError is a classified Kerberos failure. Its text is the user-facing explanation; the library error stays
// available through Unwrap and carries no secrets.
type kerberosError struct {
	failure kerberosFailure
	cause   error
}

func newKerberosError(failure kerberosFailure, cause error) *kerberosError {
	return &kerberosError{failure: failure, cause: cause}
}

func (e *kerberosError) Error() string {
	if message, ok := kerberosFailureMessages[e.failure]; ok {
		return message
	}
	if e.cause == nil {
		return "Kerberos 认证失败"
	}
	return "Kerberos 认证失败：" + e.cause.Error()
}

func (e *kerberosError) Unwrap() error {
	return e.cause
}

func kerberosFailureForCode(code int32) kerberosFailure {
	switch code {
	case errorcode.KRB_AP_ERR_SKEW:
		return kerberosFailureClockSkew
	case errorcode.KDC_ERR_C_PRINCIPAL_UNKNOWN:
		return kerberosFailurePrincipalUnknown
	case errorcode.KDC_ERR_S_PRINCIPAL_UNKNOWN:
		return kerberosFailureSPNUnknown
	case errorcode.KDC_ERR_PREAUTH_FAILED:
		return kerberosFailureBadPassword
	case errorcode.KDC_ERR_CLIENT_REVOKED:
		return kerberosFailureAccountRevoked
	case errorcode.KDC_ERR_KEY_EXPIRED:
		return kerberosFailurePasswordExpired
	case errorcode.KDC_ERR_ETYPE_NOSUPP:
		return kerberosFailureEncryptionType
	case errorcode.KDC_ERR_WRONG_REALM:
		return kerberosFailureWrongRealm
	case errorcode.KRB_AP_ERR_MODIFIED, errorcode.KRB_AP_ERR_BAD_INTEGRITY, errorcode.KRB_AP_ERR_NOKEY,
		errorcode.KRB_AP_ERR_BADKEYVER, errorcode.KRB_AP_ERR_NOT_US:
		return kerberosFailureTicketNotAccepted
	}
	return kerberosFailureUnknown
}

func classifyKerberosError(krbError messages.KRBError) error {
	return newKerberosError(kerberosFailureForCode(krbError.ErrorCode), krbError)
}

// gokrb5 flattens the KRB-ERROR it receives from the KDC into its error text ("KRB Error: (24) ..."), so the code
// is read back from there.
var krbErrorCodePattern = regexp.MustCompile(`KRB Error: \((\d+)\)`)

// classifyKerberosClientError explains an error from obtaining a ticket. kdcConfigured says whether the KDCs were
// given explicitly or come from DNS SRV discovery.
func classifyKerberosClientError(err error, kdcConfigured bool) error {
	if err == nil {
		return nil
	}
	var classified *kerberosError
	if errors.As(err, &classified) {
		return err
	}
	var krbError messages.KRBError
	if errors.As(err, &krbError) {
		return classifyKerberosError(krbError)
	}
	text := err.Error()
	if match := krbErrorCodePattern.FindStringSubmatch(text); match != nil {
		if code, convErr := strconv.ParseInt(match[1], 10, 32); convErr == nil {
			return newKerberosError(kerberosFailureForCode(int32(code)), err)
		}
	}
	var libraryError krberror.Krberror
	if errors.As(err, &libraryError) {
		switch libraryError.RootCause {
		case krberror.NetworkingError:
			if !kdcConfigured && kerberosSRVLookupFailed(text) {
				return newKerberosError(kerberosFailureKDCNotFound, err)
			}
			return newKerberosError(kerberosFailureKDCUnreachable, err)
		case krberror.KRBMsgError:
			// The KDC answered without asking for pre-authentication and the reply did not decrypt with the
			// password's key.
			if strings.Contains(text, "password/keytab incorrect") {
				return newKerberosError(kerberosFailureBadPassword, err)
			}
		}
	}
	return newKerberosError(kerberosFailureUnknown, err)
}

var kerberosSRVPattern = regexp.MustCompile(`(?i)SRV|_kerberos\._tcp|no such host`)

func kerberosSRVLookupFailed(text string) bool {
	return kerberosSRVPattern.MatchString(text)
}
