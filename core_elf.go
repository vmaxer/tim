package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

const (
	coreBase    = 0x400000
	elfCodeOff  = 0x1000
	elfPageSize = 0x1000
)

// elfGOT places the global offset table for C imports on the page after the
// code; entries are 8 bytes, in import order.
func elfLayout(codeLen int, cimps []cImport) (int, []int) {
	got := alignUp(elfCodeOff+codeLen, elfPageSize) - elfCodeOff
	entries := make([]int, len(cimps))
	for i := range cimps {
		entries[i] = got + 8*i
	}
	return 0, entries
}

// writeCoreELF writes an executable: a read-only, executable segment holding
// the code at coreBase+0x1000, and, when the program calls C, a writable
// segment with the global offset table and what the dynamic linker needs.
func writeCoreELF(path string, arch Arch, code []byte, entry int, cimps []cImport) error {
	var machine uint16
	var flags uint32
	var interp string
	var relType uint64
	switch arch {
	case ArchX86_64:
		machine, interp, relType = 62, "/lib64/ld-linux-x86-64.so.2", 6 // R_X86_64_GLOB_DAT
	case ArchARM64:
		machine, interp, relType = 183, "/lib/ld-linux-aarch64.so.1", 1025 // R_AARCH64_GLOB_DAT
	case ArchRiscv64:
		machine, flags, interp, relType = 243, 0x5, "/lib/ld-linux-riscv64-lp64d.so.1", 2 // RVC, double-float ABI; R_RISCV_64
	default:
		return fmt.Errorf("no core ELF support for %s", arch)
	}
	le := binary.LittleEndian
	dynamic := len(cimps) > 0
	nph := 2
	if dynamic {
		nph = 5
	}
	b := make([]byte, elfCodeOff, elfCodeOff+len(code))
	copy(b, "\x7fELF\x02\x01\x01")
	le.PutUint16(b[16:], 2) // ET_EXEC
	le.PutUint16(b[18:], machine)
	le.PutUint32(b[20:], 1)
	le.PutUint64(b[24:], uint64(coreBase+elfCodeOff+entry))
	le.PutUint64(b[32:], 64) // program headers
	le.PutUint32(b[48:], flags)
	le.PutUint16(b[52:], 64)
	le.PutUint16(b[54:], 56)
	le.PutUint16(b[56:], uint16(nph))
	le.PutUint16(b[58:], 64)

	phn := 0
	phdr := func(typ, fl uint32, off, vaddr, filesz, memsz, align uint64) {
		ph := b[64+56*phn:]
		phn++
		le.PutUint32(ph[0:], typ)
		le.PutUint32(ph[4:], fl)
		le.PutUint64(ph[8:], off)
		le.PutUint64(ph[16:], vaddr)
		le.PutUint64(ph[24:], vaddr)
		le.PutUint64(ph[32:], filesz)
		le.PutUint64(ph[40:], memsz)
		le.PutUint64(ph[48:], align)
	}

	var data []byte
	var dataOff, dynSize int
	if dynamic {
		dataOff = alignUp(elfCodeOff+len(code), elfPageSize)
		dataVA := uint64(coreBase + dataOff)
		// The GOT, then the dynamic section, symbols, strings, hash and relocations.
		strs := []byte{0}
		str := func(s string) uint32 {
			off := uint32(len(strs))
			strs = append(append(strs, s...), 0)
			return off
		}
		var needed []uint32
		seen := map[string]bool{}
		for _, ci := range cimps {
			for _, n := range ci.lib.linkName(OSLinux) {
				if !seen[n] {
					seen[n] = true
					needed = append(needed, str(n))
				}
			}
		}
		syms := make([]byte, 24*(len(cimps)+1))
		for i, ci := range cimps {
			s := syms[24*(i+1):]
			le.PutUint32(s[0:], str(ci.name))
			s[4] = 0x12 // global function
		}
		interpOff := str(interp)
		nsym := len(cimps) + 1
		hash := make([]byte, 4*(2+1+nsym))
		le.PutUint32(hash[0:], 1)
		le.PutUint32(hash[4:], uint32(nsym))
		gotSize := 8 * len(cimps)
		dynSize = 16 * (len(needed) + 12)
		symOff := gotSize + dynSize
		strOff := symOff + len(syms)
		hashOff := alignUp(strOff+len(strs), 8)
		relaOff := hashOff + len(hash)
		rela := make([]byte, 24*len(cimps))
		for i := range cimps {
			r := rela[24*i:]
			le.PutUint64(r[0:], dataVA+uint64(8*i))
			le.PutUint64(r[8:], uint64(i+1)<<32|relType)
		}
		dyn := []uint64{}
		for _, n := range needed {
			dyn = append(dyn, 1, uint64(n)) // DT_NEEDED
		}
		dyn = append(dyn,
			4, dataVA+uint64(hashOff), // DT_HASH
			5, dataVA+uint64(strOff), // DT_STRTAB
			6, dataVA+uint64(symOff), // DT_SYMTAB
			7, dataVA+uint64(relaOff), // DT_RELA
			8, uint64(len(rela)), // DT_RELASZ
			9, 24, // DT_RELAENT
			10, uint64(len(strs)), // DT_STRSZ
			11, 24, // DT_SYMENT
			24, 0, // DT_BIND_NOW
			30, 8, // DT_FLAGS: DF_BIND_NOW
			21, 0, // DT_DEBUG
			0, 0) // DT_NULL
		data = make([]byte, relaOff+len(rela))
		for i, v := range dyn {
			le.PutUint64(data[gotSize+8*i:], v)
		}
		copy(data[symOff:], syms)
		copy(data[strOff:], strs)
		copy(data[hashOff:], hash)
		copy(data[relaOff:], rela)
		interpVA := dataVA + uint64(strOff) + uint64(interpOff)
		phdr(3, 4, uint64(dataOff+strOff)+uint64(interpOff), interpVA, uint64(len(interp)+1), uint64(len(interp)+1), 1) // PT_INTERP
	}
	phdr(1, 5, 0, coreBase, uint64(elfCodeOff+len(code)), uint64(elfCodeOff+len(code)), elfPageSize) // PT_LOAD R+X
	if dynamic {
		dataVA := uint64(coreBase + dataOff)
		phdr(1, 6, uint64(dataOff), dataVA, uint64(len(data)), uint64(len(data)), elfPageSize)                     // PT_LOAD R+W
		phdr(2, 6, uint64(dataOff+8*len(cimps)), dataVA+uint64(8*len(cimps)), uint64(dynSize), uint64(dynSize), 8) // PT_DYNAMIC
	}
	phdr(0x6474E551, 6, 0, 0, 0, 0, 16) // PT_GNU_STACK

	b = append(b, code...)
	if dynamic {
		b = append(b, make([]byte, dataOff-len(b))...)
		b = append(b, data...)
	}
	if err := os.WriteFile(path, b, 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}
