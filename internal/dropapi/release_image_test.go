package dropapi_test

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestReleaseProgramHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   elf.ProgType
		tag    elf.DynTag
		accept bool
	}{
		{"static PIE", elf.PT_DYNAMIC, elf.DT_NULL, true},
		{"shared dependency", elf.PT_DYNAMIC, elf.DT_NEEDED, false},
		{"runtime loader", elf.PT_INTERP, elf.DT_NULL, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := droptest.ELF(512, 2)
			order := binary.LittleEndian
			order.PutUint16(b[16:], uint16(elf.ET_DYN))
			order.PutUint16(b[56:], 2)
			clear(b[120:176])
			order.PutUint32(b[120:], uint32(tc.kind))
			order.PutUint64(b[128:], 256)
			order.PutUint64(b[152:], 16)
			order.PutUint64(b[160:], 16)
			order.PutUint64(b[256:], uint64(tc.tag))
			if ok := dropapi.ReleaseImageProblem(bytes.NewReader(b), int64(len(b))) == ""; ok != tc.accept {
				t.Errorf("accepted = %v, want %v", ok, tc.accept)
			}
		})
	}
}
