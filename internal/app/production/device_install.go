package production

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
)

// deviceInstallName is the file in the data directory that holds this
// installation's random fingerprint. It lives outside the database, so a
// database copied or restored elsewhere is recognised there and takes a new
// device ID for its scheduled jobs.
const deviceInstallName = "device-install-id"

// loadDeviceInstall reads this installation's fingerprint, creating it on
// first use. Without a writable data directory it returns a fresh value for
// this run only, which gives the database a new device ID on every start.
func loadDeviceInstall(dataDir string, logger *slog.Logger) string {
	path := filepath.Join(dataDir, deviceInstallName)
	if data, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(data)); len(value) == 32 {
			if _, err := hex.DecodeString(value); err == nil {
				return value
			}
		}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("read random device install fingerprint: %v", err))
	}
	value := hex.EncodeToString(raw)
	if err := atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, value+"\n")
		return err
	}); err != nil {
		logger.Warn("persist the device install fingerprint; scheduled jobs will be bound to a new device ID after a restart", "error", err)
	}
	return value
}
