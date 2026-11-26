package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// ManifestProbe serves one systemd-accepted socket. The caller provides only
// the digest, byte count and contents. Paths and the command are host-owned.
func ManifestProbe(input io.Reader, output io.Writer) error {
	reader := bufio.NewReaderSize(input, 128)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return errors.New("invalid probe header")
	}
	digest, length, ok := strings.Cut(strings.TrimSuffix(string(line), "\n"), " ")
	size, err := strconv.ParseInt(length, 10, 64)
	if !ok || !awsx.ValidSHA256(digest) || err != nil || size < 1 || size > dropapi.MaxReleaseBytes {
		return errors.New("invalid size or digest")
	}
	file, err := os.CreateTemp("", "drop-probe-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, copyErr := io.CopyN(file, reader, size)
	err = file.Close()
	if copyErr != nil {
		return copyErr
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nibrunnerd", "probe-manifest", "--config", "/etc/nibrunner/config.toml", "--binary", file.Name(), "--sha256", digest)
	// nibrunnerd bounds guest stdout and returns code 3 only after the guest
	// completed an isolated probe with an invalid or absent manifest.
	raw, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		return json.NewEncoder(output).Encode(map[string]string{"outcome": "invalid"})
	}
	if err != nil {
		return errors.New("NibRunner inspection failed")
	}
	if len(raw) > 16<<10 || !json.Valid(raw) {
		return errors.New("invalid NibRunner response")
	}
	return json.NewEncoder(output).Encode(struct {
		Outcome  string          `json:"outcome"`
		Manifest json.RawMessage `json:"manifest"`
	}{"success", raw})
}
