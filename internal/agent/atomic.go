package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pocketcontext-labs/pocketcontext-core/atomicfile"
)

// writeFileAtomic replaces path with data (mode 0600) through a temporary
// file in the same directory that is fsynced before the rename, then fsyncs
// the directory so the rename itself survives a crash. A reader sees the old
// file or the new one, never a mix.
func writeFileAtomic(path string, data []byte) error {
	if err := atomicfile.WriteAtomic(path, data, 0o600); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// readOptional reads a file, returning nil without error when it is missing.
func readOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// writeDesired backs up the current desired.json (when there is one) to
// desired.json.drop-prev, then atomically writes the new document. current
// is the content the caller rendered against; if the file changed since, the
// write is refused so nothing someone else wrote is overwritten unseen.
func writeDesired(path, backup string, current, next []byte) error {
	onDisk, err := readOptional(path)
	if err != nil {
		return err
	}
	if string(onDisk) != string(current) {
		return errors.New("desired.json changed while the agent was rendering; not writing, the next pull will re-read it")
	}
	if current != nil {
		if err := writeFileAtomic(backup, current); err != nil {
			return err
		}
	}
	return writeFileAtomic(path, next)
}
