package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// Imports of a core program on Windows, in the order runtime/rt_windows.c
// indexes them; each DLL's list ends with a null entry.
var peImportList = []struct {
	dll   string
	funcs []string
}{
	{"kernel32.dll", []string{"GetStdHandle", "WriteFile", "ReadFile", "CreateFileA", "CloseHandle", "ExitProcess",
		"VirtualAlloc", "GetCommandLineA", "GetEnvironmentStringsA"}},
	{"advapi32.dll", []string{"SystemFunction036"}}, // RtlGenRandom
}

const (
	peTextRVA  = 0x1000
	peHeaders  = 0x400
	peFileAlgn = 0x200
	peImage    = 0x140000000
)

func alignUp(n, a int) int { return (n + a - 1) &^ (a - 1) }

// peImportsAt places the import address table at the start of the section
// after the code.
func peImportsAt(codeLen int) int { return alignUp(peTextRVA+codeLen, 0x1000) - peTextRVA }

// peIdata builds the import section for rva: the import address table, the
// import directory, the lookup tables and the names.
func peIdata(rva int) (data []byte, dirOff, dirSize, iatSize int) {
	le := binary.LittleEndian
	nThunks := 0
	for _, d := range peImportList {
		nThunks += len(d.funcs) + 1
	}
	iatSize = 8 * nThunks
	dirOff = iatSize
	dirSize = 20 * (len(peImportList) + 1)
	iltOff := dirOff + dirSize
	namesOff := iltOff + iatSize
	data = make([]byte, namesOff)
	thunk := 0
	for i, d := range peImportList {
		dir := dirOff + 20*i
		le.PutUint32(data[dir:], uint32(rva+iltOff+8*thunk))
		le.PutUint32(data[dir+16:], uint32(rva+8*thunk))
		for _, f := range d.funcs {
			hint := len(data)
			data = append(data, 0, 0)
			data = append(data, f...)
			data = append(data, 0)
			if len(data)%2 == 1 {
				data = append(data, 0)
			}
			le.PutUint64(data[8*thunk:], uint64(rva+hint))
			le.PutUint64(data[iltOff+8*thunk:], uint64(rva+hint))
			thunk++
		}
		thunk++ // null terminator
		le.PutUint32(data[dir+12:], uint32(rva+len(data)))
		data = append(data, d.dll...)
		data = append(data, 0)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	return data, dirOff, dirSize, iatSize
}

// writeCorePE writes a Windows console executable.
func writeCorePE(path string, arch Arch, code []byte, entry int) error {
	var machine uint16
	switch arch {
	case ArchX86_64:
		machine = 0x8664
	case ArchARM64:
		machine = 0xAA64
	default:
		return fmt.Errorf("no core PE support for %s", arch)
	}
	le := binary.LittleEndian
	idataRVA := peTextRVA + peImportsAt(len(code))
	idata, dirOff, dirSize, iatSize := peIdata(idataRVA)
	textRaw := alignUp(len(code), peFileAlgn)
	idataRaw := alignUp(len(idata), peFileAlgn)
	imageSize := alignUp(idataRVA+len(idata), 0x1000)

	b := make([]byte, peHeaders+textRaw+idataRaw)
	copy(b, "MZ")
	le.PutUint32(b[0x3C:], 0x40)
	copy(b[0x40:], "PE\x00\x00")
	coff := b[0x44:]
	le.PutUint16(coff[0:], machine)
	le.PutUint16(coff[2:], 2)
	le.PutUint16(coff[16:], 240)
	le.PutUint16(coff[18:], 0x0023) // relocations stripped, executable, large address aware

	opt := b[0x58:]
	le.PutUint16(opt[0:], 0x20B)
	opt[2] = 14
	le.PutUint32(opt[4:], uint32(textRaw))
	le.PutUint32(opt[8:], uint32(idataRaw))
	le.PutUint32(opt[16:], uint32(peTextRVA+entry))
	le.PutUint32(opt[20:], peTextRVA)
	le.PutUint64(opt[24:], peImage)
	le.PutUint32(opt[32:], 0x1000)
	le.PutUint32(opt[36:], peFileAlgn)
	le.PutUint16(opt[40:], 6)
	le.PutUint16(opt[48:], 6)
	le.PutUint32(opt[56:], uint32(imageSize))
	le.PutUint32(opt[60:], peHeaders)
	le.PutUint16(opt[68:], 3)      // console
	le.PutUint16(opt[70:], 0x8100) // NX compatible, terminal server aware
	// The whole stack is committed up front, so frames of any size need no probes.
	le.PutUint64(opt[72:], 8<<20)
	le.PutUint64(opt[80:], 8<<20)
	le.PutUint64(opt[88:], 1<<20)
	le.PutUint64(opt[96:], 0x1000)
	le.PutUint32(opt[108:], 16)
	le.PutUint32(opt[112+8:], uint32(idataRVA+dirOff))
	le.PutUint32(opt[112+12:], uint32(dirSize))
	le.PutUint32(opt[112+12*8:], uint32(idataRVA))
	le.PutUint32(opt[112+12*8+4:], uint32(iatSize))

	section := func(at int, name string, vsize, rva, raw, rawPtr int, flags uint32) {
		s := b[at:]
		copy(s, name)
		le.PutUint32(s[8:], uint32(vsize))
		le.PutUint32(s[12:], uint32(rva))
		le.PutUint32(s[16:], uint32(raw))
		le.PutUint32(s[20:], uint32(rawPtr))
		le.PutUint32(s[36:], flags)
	}
	section(0x148, ".text", len(code), peTextRVA, textRaw, peHeaders, 0x60000020)
	section(0x148+40, ".idata", len(idata), idataRVA, idataRaw, peHeaders+textRaw, 0xC0000040)

	copy(b[peHeaders:], code)
	copy(b[peHeaders+textRaw:], idata)
	if err := os.WriteFile(path, b, 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}
