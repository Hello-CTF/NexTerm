package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Failure kinds reported by DescribeTestFailure.
const (
	TestKindAuth        = "auth"
	TestKindDatabase    = "database"
	TestKindTLS         = "tls"
	TestKindTimeout     = "timeout"
	TestKindRefused     = "refused"
	TestKindDNS         = "dns"
	TestKindUnreachable = "unreachable"
	TestKindNetwork     = "network"
	TestKindTunnel      = "tunnel"
	TestKindVault       = "vault"
	TestKindConfig      = "config"
	TestKindNotFound    = "not_found"
	TestKindCanceled    = "canceled"
	TestKindInternal    = "internal"
)

// TestAsset opens and immediately closes a connection for a stored database
// asset, using the same credential, TLS and tunnel resolution as Connect. The
// handle is never registered, no SQL is run beyond the driver's own ping, and
// TLS verification settings are honoured exactly as configured.
func (s *Service) TestAsset(ctx context.Context, assetID string, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	connection, err := s.open(ctx, ConnectArgs{AssetID: assetID})
	if err != nil {
		return err
	}
	// The login already succeeded; a failing close must not turn it into a failed test.
	_ = connection.Close()
	return nil
}

// DescribeTestFailure maps a TestAsset error to a stable kind and a plain
// message that names the next step. It never echoes the underlying error text,
// which may contain host details or credential material. service is the asset
// kind (mysql, postgres, redis) and endpoint is a host:port label.
func DescribeTestFailure(err error, service, endpoint string) (kind, message string) {
	if err == nil {
		return "", ""
	}
	name := serviceLabel(service)
	target := name
	if endpoint != "" {
		target = name + " " + endpoint
	}
	if errors.Is(err, context.Canceled) {
		return TestKindCanceled, "已取消"
	}
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) {
		switch ipcErr.Code {
		case ipc.CodeVaultLocked, ipc.CodeVaultNotInit, ipc.CodeBadMasterPass, ipc.CodeDecrypt:
			return TestKindVault, "密码库还没有解锁，或这条资产绑定的凭据读不出来。请先解锁密码库，再检查资产绑定的凭据"
		case ipc.CodeNotFound:
			return TestKindNotFound, "资产不存在，可能已被删除，请刷新后重试"
		case ipc.CodeBadParam:
			return TestKindConfig, "资产配置不完整或无效，请检查主机、端口、用户名、数据库名和 TLS 设置"
		case ipc.CodeUnsupported, ipc.CodeDisconnected, ipc.CodeForbidden:
			return TestKindTunnel, target + " 需要经过 SSH 跳板连接，但跳板不可用。请先连接作为跳板的 SSH 资产，再重新测试"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TestKindTimeout, "连接 " + target + " 超时，请检查网络、防火墙和服务是否在运行"
	}
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1044, 1045, 1698, 1130:
			return TestKindAuth, target + " 已连通，但账号或密码被拒绝。请检查用户名、密码，以及该账号是否允许从本机登录"
		case 1049:
			return TestKindDatabase, target + " 已连通，但指定的数据库不存在，请检查资产里的数据库名"
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "28"):
			return TestKindAuth, target + " 已连通，但账号或密码被拒绝。请检查用户名、密码，以及 pg_hba 是否允许该账号登录"
		case pgErr.Code == "3D000":
			return TestKindDatabase, target + " 已连通，但指定的数据库不存在，请检查资产里的数据库名"
		}
	}
	if isTLSFailure(err) {
		return TestKindTLS, target + " 的 TLS 校验没有通过（证书不受信任、主机名不匹配，或服务器不支持 TLS）。连接测试不会自动信任证书，请在资产的 TLS 设置里提供 CA 证书、核对服务器名称，或确认服务器已启用 TLS"
	}
	if service == "redis" && isRedisAuthFailure(err) {
		return TestKindAuth, target + " 已连通，但需要密码或账号密码被拒绝。请检查资产绑定的凭据和用户名"
	}
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return TestKindDNS, "无法解析 " + target + " 的主机名，请检查地址是否拼写正确"
	case errors.Is(err, syscall.ECONNREFUSED):
		return TestKindRefused, target + " 拒绝了连接，请确认服务已启动且端口正确"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return TestKindUnreachable, "无法连到 " + target + "，请检查网络、VPN 和防火墙"
	case errors.As(err, &netErr) && netErr.Timeout():
		return TestKindTimeout, "连接 " + target + " 超时，请检查网络、防火墙和服务是否在运行"
	case errors.As(err, &netErr), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed),
		errors.Is(err, driver.ErrBadConn), errors.Is(err, mysqlDriver.ErrInvalidConn):
		return TestKindNetwork, "无法连到 " + target + "，或连接被中途断开，请检查网络和防火墙"
	}
	return TestKindInternal, "测试没有完成，请重试；如果反复出现，请查看应用日志"
}

func serviceLabel(service string) string {
	switch strings.ToLower(service) {
	case "mysql":
		return "MySQL"
	case "postgres":
		return "PostgreSQL"
	case "redis":
		return "Redis"
	}
	return "数据库"
}

func isTLSFailure(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		errors.As(err, &verification) || errors.As(err, &record) || errors.As(err, &alert) ||
		errors.Is(err, mysqlDriver.ErrNoTLS) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "x509:") || strings.Contains(text, "tls:") || strings.Contains(text, "server refused tls") ||
		strings.Contains(text, "ssl is not enabled")
}

func isRedisAuthFailure(err error) bool {
	text := strings.ToUpper(err.Error())
	for _, marker := range []string{"WRONGPASS", "NOAUTH", "INVALID PASSWORD", "INVALID USERNAME-PASSWORD", "AUTHENTICATION REQUIRED", "NOPERM"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
