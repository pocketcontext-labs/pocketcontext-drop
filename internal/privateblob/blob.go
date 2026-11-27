// Package privateblob encrypts developer executables in the shared node store.
// Only the assigned node receives a release key via its authenticated desired
// document. The app master stays in the control plane.
package privateblob

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

const chunkSize = 1024 * 1024
const tagSize = 16

func NewMaster() string {
	var key [32]byte
	rand.Read(key[:])
	return hex.EncodeToString(key[:])
}

// Key derives a different AES-256 key for each app and distinct plaintext.
// Re-uploading the same executable within an app reproduces the same blob.
func Key(master, digest string) (string, error) {
	m, err := hex.DecodeString(master)
	if err != nil || len(m) != 32 {
		return "", errors.New("private app key is invalid")
	}
	d, err := hex.DecodeString(digest)
	if err != nil || len(d) != sha256.Size {
		return "", errors.New("invalid executable digest")
	}
	h := hmac.New(sha256.New, m)
	h.Write([]byte("drop/private-release/v1\x00"))
	h.Write(d)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func StoredSize(size int64) int64 { return size + (size+chunkSize-1)/chunkSize*tagSize }

// Seal and Open process fixed-size GCM chunks. Each chunk index supplies a
// unique nonce under this plaintext-specific key. The authenticated desired
// document fixes the total plaintext size and its digest.
func Seal(w io.Writer, r io.Reader, key string, size int64) error {
	return transform(w, r, key, size, false)
}
func Open(w io.Writer, r io.Reader, key string, size int64) error {
	return transform(w, r, key, size, true)
}
func transform(w io.Writer, r io.Reader, key string, size int64, open bool) error {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 || size <= 0 {
		return errors.New("invalid private release parameters")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	buf := make([]byte, min(size, int64(chunkSize))+tagSize)
	var nonce [12]byte
	for i := uint64(0); size > 0; i++ {
		n := min(size, int64(chunkSize))
		input := n
		if open {
			input += tagSize
		}
		if _, err := io.ReadFull(r, buf[:input]); err != nil {
			return err
		}
		binary.BigEndian.PutUint64(nonce[4:], i)
		var output []byte
		if open {
			output, err = gcm.Open(buf[:0], nonce[:], buf[:input], nil)
			if err != nil {
				return errors.New("private release authentication failed")
			}
		} else {
			output = gcm.Seal(buf[:0], nonce[:], buf[:input], nil)
		}
		if _, err := w.Write(output); err != nil {
			return err
		}
		size -= n
	}
	var extra [1]byte
	n, err := io.ReadFull(r, extra[:])
	if n != 0 || err != io.EOF {
		return errors.New("unexpected private release length")
	}
	return nil
}
