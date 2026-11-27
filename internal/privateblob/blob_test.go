package privateblob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestAppAndReleaseIsolation(t *testing.T) {
	plain := bytes.Repeat([]byte("private ELF test data"), 82317)
	hash := sha256.Sum256(plain)
	digest := hex.EncodeToString(hash[:])
	key, err := Key(NewMaster(), digest)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Key(NewMaster(), digest)
	if err != nil {
		t.Fatal(err)
	}
	var blob bytes.Buffer
	if err := Seal(&blob, bytes.NewReader(plain), key, int64(len(plain))); err != nil {
		t.Fatal(err)
	}
	if n := int64(blob.Len()); n != StoredSize(int64(len(plain))) || bytes.Contains(blob.Bytes(), plain[:64]) {
		t.Fatal("ciphertext size or contents")
	}
	var restored bytes.Buffer
	if err := Open(&restored, bytes.NewReader(blob.Bytes()), key, int64(len(plain))); err != nil || !bytes.Equal(plain, restored.Bytes()) {
		t.Fatal("round trip", err)
	}
	if err := Open(io.Discard, bytes.NewReader(blob.Bytes()), other, int64(len(plain))); err == nil {
		t.Fatal("foreign key accepted")
	}
	// A valid prefix, trailing data, and a corrupted chunk are all refused.
	for _, bad := range [][]byte{blob.Bytes()[:blob.Len()-tagSize], append(bytes.Clone(blob.Bytes()), 0)} {
		if err := Open(io.Discard, bytes.NewReader(bad), key, int64(len(plain))); err == nil {
			t.Fatal("wrong length accepted")
		}
	}
	blob.Bytes()[chunkSize+tagSize] ^= 1
	if err := Open(io.Discard, bytes.NewReader(blob.Bytes()), key, int64(len(plain))); err == nil {
		t.Fatal("corrupt second chunk accepted")
	}
}
