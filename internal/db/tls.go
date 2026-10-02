package db

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

func makeTLSConfig(host string, options *TLSOptions) (*tls.Config, error) {
	if options == nil {
		return nil, nil
	}

	insecure := options.InsecureSkipVerify
	switch strings.ToLower(strings.TrimSpace(options.Mode)) {
	case "off", "disabled", "disable", "false":
		return nil, nil
	case "", "required", "require", "verify-full":
	case "skip-verify":
		insecure = true
	default:
		return nil, fmt.Errorf("不支持的 TLS 模式 %s", options.Mode)
	}

	config := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         options.ServerName,
		InsecureSkipVerify: insecure,
	}
	if config.ServerName == "" {
		config.ServerName = host
	}
	if options.CAFile != "" {
		pem, err := os.ReadFile(options.CAFile)
		if err != nil {
			return nil, fmt.Errorf("读取 TLS CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("TLS CA 文件没有有效证书")
		}
		config.RootCAs = roots
	}
	return config, nil
}
