package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

const coreBase = 0x400000

// writeCoreELF writes a static executable: one read-only, executable segment
// holding the code at coreBase+0x1000; the heap comes from mmap at run time.
func writeCoreELF(path string, arch Arch, code []byte, entry int) error {
	var machine uint16
	var flags uint32
	switch arch {
	case ArchX86_64:
		machine = 62
	case ArchARM64:
		machine = 183
	case ArchRiscv64:
		machine, flags = 243, 0x5 // RVC, double-float ABI
	default:
		return fmt.Errorf("no core ELF support for %s", arch)
	}
	const codeOff = 0x1000
	le := binary.LittleEndian
	b := make([]byte, codeOff, codeOff+len(code))
	copy(b, "\x7fELF\x02\x01\x01")
	le.PutUint16(b[16:], 2) // ET_EXEC
	le.PutUint16(b[18:], machine)
	le.PutUint32(b[20:], 1)
	le.PutUint64(b[24:], uint64(coreBase+codeOff+entry))
	le.PutUint64(b[32:], 64) // program headers
	le.PutUint32(b[48:], flags)
	le.PutUint16(b[52:], 64)
	le.PutUint16(b[54:], 56)
	le.PutUint16(b[56:], 2)
	le.PutUint16(b[58:], 64)

	ph := b[64:]
	le.PutUint32(ph[0:], 1) // PT_LOAD
	le.PutUint32(ph[4:], 5) // R+X
	le.PutUint64(ph[16:], coreBase)
	le.PutUint64(ph[24:], coreBase)
	le.PutUint64(ph[32:], uint64(codeOff+len(code)))
	le.PutUint64(ph[40:], uint64(codeOff+len(code)))
	le.PutUint64(ph[48:], 0x1000)

	ph = b[64+56:]
	le.PutUint32(ph[0:], 0x6474E551) // PT_GNU_STACK
	le.PutUint32(ph[4:], 6)          // R+W
	le.PutUint64(ph[48:], 16)

	b = append(b, code...)
	if err := os.WriteFile(path, b, 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}
