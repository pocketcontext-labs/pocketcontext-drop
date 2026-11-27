package dropserver

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"os"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// spoolBuild returns the executable, never the archive, for storage and probing.
// Like spool, callers own any returned file, including on error.
func spoolBuild(body io.Reader, max int64) (*os.File, int64, string, error) {
	compressed := &io.LimitedReader{R: body, N: max + 1}
	input := bufio.NewReader(compressed)
	signature, _ := input.Peek(2)
	if !bytes.Equal(signature, []byte{0x1f, 0x8b}) {
		return spool(input, max)
	}
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, 0, "", invalidRelease("Invalid gzip archive.")
	}
	defer gz.Close()
	const expandedLimit = 384 << 20
	expanded := &io.LimitedReader{R: gz, N: expandedLimit + 1}
	archive := tar.NewReader(expanded)
	var file *os.File
	var size int64
	var digest string
	for count := 0; ; count++ {
		entry, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || count >= 4096 {
			return file, 0, "", invalidRelease("Invalid tar archive or too many entries.")
		}
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Typeflag != tar.TypeReg {
			return file, 0, "", invalidRelease("The archive must contain only regular files and directories.")
		}
		// Members are read directly from the stream. Their names never become filesystem paths.
		member := bufio.NewReader(archive)
		header, _ := member.Peek(dropapi.ELFHeaderBytes)
		if dropapi.ReleaseHeaderProblem(header) != "" {
			continue
		}
		if file != nil {
			return file, 0, "", invalidRelease("This archive contains multiple executables. Upload the intended executable separately.")
		}
		file, size, digest, err = spool(member, max)
		if err != nil {
			return file, 0, "", err
		}
	}
	// tar can finish before gzip has consumed padding or verified the trailer.
	if _, err := io.Copy(io.Discard, expanded); compressed.N == 0 {
		return file, 0, "", releaseTooLarge(max)
	} else if err != nil || expanded.N == 0 {
		return file, 0, "", invalidRelease("The tar.gz archive is corrupt or exceeds the expanded-size limit.")
	}
	if file == nil {
		return nil, 0, "", invalidRelease("The tar.gz archive must contain a standalone Linux amd64 executable.")
	}
	return file, size, digest, nil
}
