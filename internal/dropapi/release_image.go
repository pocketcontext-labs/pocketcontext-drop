package dropapi

import (
	"bufio"
	"debug/elf"
	"encoding/binary"
	"io"
)

// ReleaseImageProblem checks the loader-facing parts of a spooled executable.
// Section headers (including debug data) aren't needed to start a process.
func ReleaseImageProblem(file io.ReaderAt, size int64) string {
	var hdr [ELFHeaderBytes]byte
	n, err := file.ReadAt(hdr[:], 0)
	if err != nil && err != io.EOF {
		return "The release has an incomplete ELF header."
	}
	if problem := ReleaseHeaderProblem(hdr[:n]); problem != "" {
		return problem
	}
	order := binary.LittleEndian
	const progSize = 56 // sizeof(Elf64_Phdr)
	count := int(order.Uint16(hdr[56:58]))
	start := order.Uint64(hdr[32:40])
	if size < ELFHeaderBytes || order.Uint32(hdr[20:24]) != 1 || order.Uint16(hdr[52:54]) != ELFHeaderBytes ||
		order.Uint16(hdr[54:56]) != progSize || count == 0 || count*progSize > 65536 ||
		start < ELFHeaderBytes || start > uint64(size) || uint64(count*progSize) > uint64(size)-start {
		return "The ELF program header table is missing or invalid."
	}
	table := make([]byte, count*progSize)
	if _, err := file.ReadAt(table, int64(start)); err != nil {
		return "The ELF program header table is incomplete."
	}
	entry := order.Uint64(hdr[24:32])
	hasEntry, hasDynamic := false, false
	for len(table) > 0 {
		p := table[:progSize]
		table = table[progSize:]
		kind := elf.ProgType(order.Uint32(p[:4]))
		offset, address := order.Uint64(p[8:16]), order.Uint64(p[16:24])
		length, memory := order.Uint64(p[32:40]), order.Uint64(p[40:48])
		switch kind {
		case elf.PT_INTERP:
			return "A release must be statically linked and run without an ELF interpreter."
		case elf.PT_LOAD, elf.PT_DYNAMIC:
			if offset > uint64(size) || length > uint64(size)-offset || length > memory {
				return "The ELF program segment is incomplete."
			}
			if kind == elf.PT_DYNAMIC {
				if hasDynamic || !standaloneDynamic(file, int64(offset), int64(length)) {
					return "The release must have a valid dynamic table without shared library dependencies."
				}
				hasDynamic = true
			} else if elf.ProgFlag(order.Uint32(p[4:8]))&elf.PF_X != 0 && entry != 0 && entry >= address && entry-address < length {
				hasEntry = true
			}
		}
	}
	if !hasEntry {
		return "The ELF entry point must be inside a file-backed executable segment."
	}
	return ""
}

// Static position-independent executables may contain relocations.
func standaloneDynamic(file io.ReaderAt, offset, length int64) bool {
	reader := bufio.NewReader(io.NewSectionReader(file, offset, length))
	var dyn [16]byte
	for {
		if _, err := io.ReadFull(reader, dyn[:]); err != nil {
			return false
		}
		switch elf.DynTag(binary.LittleEndian.Uint64(dyn[:8])) {
		case elf.DT_NULL:
			return true
		case elf.DT_NEEDED:
			return false
		}
	}
}
