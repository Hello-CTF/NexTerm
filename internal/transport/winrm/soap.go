package winrm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/go-ntlmssp"
	gowinrm "github.com/masterzen/winrm"
	"github.com/masterzen/winrm/soap"
)

type libraryRunner struct {
	endpoint       string
	principal      string
	password       string
	parameters     *gowinrm.Parameters
	httpClient     *http.Client
	httpTransport  *http.Transport
	requestTimeout time.Duration
}

func newLibraryRunner(config Config) (*libraryRunner, error) {
	proxy, err := proxyFunction(config.ProxyURL)
	if err != nil {
		return nil, err
	}
	var rootCAs *x509.CertPool
	if len(config.CACert) != 0 {
		rootCAs = x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(config.CACert) {
			return nil, fmt.Errorf("build WinRM client: unable to read CA certificates")
		}
	}
	httpTransport := &http.Transport{
		Proxy: proxy,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.AcceptInvalidCerts,
			ServerName:         config.TLSServerName,
			RootCAs:            rootCAs,
		},
		ResponseHeaderTimeout: config.RequestTimeout,
	}
	var roundTripper http.RoundTripper = httpTransport
	if config.Auth == AuthNTLM {
		roundTripper = &ntlmssp.Negotiator{RoundTripper: httpTransport}
	}
	scheme := "http"
	if config.UseTLS {
		scheme = "https"
	}
	endpoint := (&url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(config.Host, strconv.Itoa(config.Port)),
		Path:   "/wsman",
	}).String()
	return &libraryRunner{
		endpoint:       endpoint,
		principal:      config.principal(),
		password:       config.Password,
		parameters:     gowinrm.NewParameters("PT60S", "en-US", 153600),
		httpClient:     &http.Client{Transport: roundTripper},
		httpTransport:  httpTransport,
		requestTimeout: config.RequestTimeout,
	}, nil
}

func (r *libraryRunner) Close() error {
	r.httpTransport.CloseIdleConnections()
	return nil
}

func (r *libraryRunner) post(ctx context.Context, message *soap.SoapMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	requestCtx := ctx
	cancel := func() {}
	if r.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, r.requestTimeout)
	}
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, r.endpoint, strings.NewReader(message.String()))
	if err != nil {
		return "", fmt.Errorf("create WinRM SOAP request: %w", err)
	}
	request.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	request.SetBasicAuth(r.principal, r.password)
	response, err := r.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("WinRM SOAP request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("read WinRM SOAP response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("WinRM SOAP HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "application/soap+xml") {
		return "", fmt.Errorf("WinRM SOAP response has invalid content type %q", response.Header.Get("Content-Type"))
	}
	return string(body), nil
}

func (r *libraryRunner) createShell(ctx context.Context) (string, error) {
	request := gowinrm.NewOpenShellRequest(r.endpoint, r.parameters)
	defer request.Free()
	response, err := r.post(ctx, request)
	if err != nil {
		return "", err
	}
	shellID, err := gowinrm.ParseOpenShellResponse(response)
	if err != nil {
		return "", fmt.Errorf("parse WinRM shell creation: %w", err)
	}
	return shellID, nil
}

func (r *libraryRunner) executeCommand(ctx context.Context, shellID, command string) (string, error) {
	request := gowinrm.NewExecuteCommandRequest(r.endpoint, shellID, command, nil, r.parameters)
	defer request.Free()
	response, err := r.post(ctx, request)
	if err != nil {
		return "", err
	}
	commandID, err := gowinrm.ParseExecuteCommandResponse(response)
	if err != nil {
		return "", fmt.Errorf("parse WinRM command execution: %w", err)
	}
	return commandID, nil
}

func (r *libraryRunner) receive(ctx context.Context, shellID, commandID string, stdout, stderr io.Writer) (bool, int, error) {
	request := gowinrm.NewGetOutputRequest(r.endpoint, shellID, commandID, "stdout stderr", r.parameters)
	defer request.Free()
	response, err := r.post(ctx, request)
	if err != nil {
		return false, 0, err
	}
	finished, exitCode, err := gowinrm.ParseSlurpOutputErrResponse(response, stdout, stderr)
	if err != nil {
		return false, 0, fmt.Errorf("parse WinRM command output: %w", err)
	}
	return finished, exitCode, nil
}

func (r *libraryRunner) signalCommand(ctx context.Context, shellID, commandID string) error {
	request := gowinrm.NewSignalRequest(r.endpoint, shellID, commandID, r.parameters)
	defer request.Free()
	_, err := r.post(ctx, request)
	return err
}

func (r *libraryRunner) deleteShell(ctx context.Context, shellID string) error {
	request := gowinrm.NewDeleteShellRequest(r.endpoint, shellID, r.parameters)
	defer request.Free()
	_, err := r.post(ctx, request)
	return err
}

func (r *libraryRunner) RunPowerShell(ctx context.Context, script string, stdout, stderr io.Writer) (exitCode int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	command := gowinrm.Powershell(script)
	if command == "" {
		return 1, fmt.Errorf("encode PowerShell command")
	}
	shellID, err := r.createShell(ctx)
	if err != nil {
		return 1, err
	}
	commandID := ""
	finished := false
	defer func() {
		var cleanupErrors []error
		if commandID != "" && !finished {
			if err := r.signalCommand(ctx, shellID, commandID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("signal WinRM command: %w", err))
			}
		}
		if err := r.deleteShell(ctx, shellID); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete WinRM shell: %w", err))
		}
		if len(cleanupErrors) != 0 {
			returnErr = errors.Join(append([]error{returnErr}, cleanupErrors...)...)
		}
	}()
	commandID, err = r.executeCommand(ctx, shellID, command)
	if err != nil {
		return 1, err
	}
	for {
		var commandFinished bool
		commandFinished, exitCode, err = r.receive(ctx, shellID, commandID, stdout, stderr)
		if err != nil {
			return 1, err
		}
		if commandFinished {
			finished = true
			return exitCode, nil
		}
	}
}
