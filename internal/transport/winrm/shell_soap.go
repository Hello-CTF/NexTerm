package winrm

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	gowinrm "github.com/masterzen/winrm"
)

const (
	signalTerminate  = "http://schemas.microsoft.com/wbem/wsman/1/windows/shell/signal/terminate"
	signalCtrlC      = "http://schemas.microsoft.com/wbem/wsman/1/windows/shell/signal/ctrl_c"
	commandStateDone = "http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Done"
	// WS-Management fault codes: the Receive wait elapsed without output, and
	// the per-user shell quota (MaxShellsPerUser) is exhausted.
	faultOperationTimeout = "2150858793"
	faultShellQuota       = "2150859174"
)

// streamChunk is one Stream element of a Receive response.
type streamChunk struct {
	stderr bool
	data   []byte
}

// receiveResult keeps the stream chunks in document order, so stdout and
// stderr interleave the way the server reported them.
type receiveResult struct {
	chunks   []streamChunk
	done     bool
	exitCode int
}

func (r *libraryRunner) sendInput(ctx context.Context, shellID, commandID string, data []byte) error {
	request := gowinrm.NewSendInputRequest(r.endpoint, shellID, commandID, data, false, r.parameters)
	defer request.Free()
	_, err := r.post(ctx, request)
	return err
}

// receiveStreams long-polls for output; the server answers when output is
// available or after wait with an operation-timeout fault.
func (r *libraryRunner) receiveStreams(ctx context.Context, shellID, commandID string, wait time.Duration) (receiveResult, error) {
	seconds := int(wait / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	parameters := *r.parameters
	parameters.Timeout = "PT" + strconv.Itoa(seconds) + "S"
	request := gowinrm.NewGetOutputRequest(r.endpoint, shellID, commandID, "stdout stderr", &parameters)
	defer request.Free()
	response, err := r.post(ctx, request)
	if err != nil {
		return receiveResult{}, err
	}
	result, err := parseReceiveResponse(response)
	if err != nil {
		return receiveResult{}, fmt.Errorf("parse WinRM command output: %w", err)
	}
	return result, nil
}

func (r *libraryRunner) signal(ctx context.Context, shellID, commandID, code string) error {
	request := gowinrm.NewSignalRequest(r.endpoint, shellID, commandID, r.parameters)
	defer request.Free()
	body := request.String()
	if code != signalTerminate {
		body = strings.Replace(body, signalTerminate, code, 1)
	}
	_, err := r.postBody(ctx, body)
	return err
}

func parseReceiveResponse(response string) (receiveResult, error) {
	var result receiveResult
	decoder := xml.NewDecoder(strings.NewReader(response))
	var stream *streamChunk
	var text strings.Builder
	inExitCode := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return receiveResult{}, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "Stream":
				stream = &streamChunk{}
				for _, attribute := range element.Attr {
					if attribute.Name.Local == "Name" {
						stream.stderr = attribute.Value == "stderr"
					}
				}
				text.Reset()
			case "CommandState":
				for _, attribute := range element.Attr {
					if attribute.Name.Local == "State" && attribute.Value == commandStateDone {
						result.done = true
					}
				}
			case "ExitCode":
				inExitCode = true
				text.Reset()
			}
		case xml.CharData:
			if stream != nil || inExitCode {
				text.Write(element)
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "Stream":
				if stream != nil {
					data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text.String()))
					if err != nil {
						return receiveResult{}, fmt.Errorf("decode stream: %w", err)
					}
					if len(data) > 0 {
						stream.data = data
						result.chunks = append(result.chunks, *stream)
					}
					stream = nil
				}
			case "ExitCode":
				inExitCode = false
				if code, err := strconv.Atoi(strings.TrimSpace(text.String())); err == nil {
					result.exitCode = code
				}
			}
		}
	}
}

func soapFaultContains(err error, needles ...string) bool {
	var httpErr *soapHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	for _, needle := range needles {
		if strings.Contains(httpErr.body, needle) {
			return true
		}
	}
	return false
}

func isOperationTimeout(err error) bool {
	return soapFaultContains(err, faultOperationTimeout, "w:TimedOut")
}

func isShellQuota(err error) bool {
	if soapFaultContains(err, faultShellQuota, "MaxShellsPerUser") {
		return true
	}
	var httpErr *soapHTTPError
	return errors.As(err, &httpErr) && strings.Contains(httpErr.body, "maximum number of") && strings.Contains(httpErr.body, "shells")
}
