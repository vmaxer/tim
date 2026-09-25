package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// libSystem functions of a core program on macOS, in the order
// runtime/rt_darwin.c indexes them.
var machoImportList = []string{"_write", "_read", "_open", "_creat", "_close", "_exit", "_mmap", "_getentropy"}

const (
	machoBase    = 0x100000000
	machoPage    = 0x4000
	machoCodeOff = 0x4000
)

func machoTextSize(codeLen int) int { return alignUp(machoCodeOff+codeLen, machoPage) }

// machoImportsAt places the global offset table at the start of __DATA.
func machoImportsAt(codeLen int) int { return machoTextSize(codeLen) - machoCodeOff }

// machoFixups builds the chained fixups that bind every __got entry.
func machoFixups(dataSegOff int) []byte {
	le := binary.LittleEndian
	n := len(machoImportList)
	const startsOff, segInfo = 32, 24
	importsOff := startsOff + segInfo + 24
	symbolsOff := importsOff + 4*n
	b := make([]byte, symbolsOff, symbolsOff+128)
	le.PutUint32(b[4:], startsOff)
	le.PutUint32(b[8:], uint32(importsOff))
	le.PutUint32(b[12:], uint32(symbolsOff))
	le.PutUint32(b[16:], uint32(n))
	le.PutUint32(b[20:], 1) // DYLD_CHAINED_IMPORT
	// starts in image: __PAGEZERO, __TEXT, __DATA, __LINKEDIT
	le.PutUint32(b[startsOff:], 4)
	le.PutUint32(b[startsOff+4+4*2:], segInfo)
	seg := b[startsOff+segInfo:]
	le.PutUint32(seg[0:], 24)
	le.PutUint16(seg[4:], machoPage)
	le.PutUint16(seg[6:], 2) // DYLD_CHAINED_PTR_64
	le.PutUint64(seg[8:], uint64(dataSegOff))
	le.PutUint16(seg[20:], 1) // one page, whose chain starts at offset 0
	b = append(b, 0)
	for i, name := range machoImportList {
		le.PutUint32(b[importsOff+4*i:], 1|uint32(len(b)-symbolsOff)<<9) // libSystem, name offset
		b = append(b, name...)
		b = append(b, 0)
	}
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	return b
}

// writeCoreMachO writes a signed macOS arm64 executable.
func writeCoreMachO(path string, arch Arch, code []byte, entry int) error {
	if arch != ArchARM64 {
		return fmt.Errorf("no core Mach-O support for %s", arch)
	}
	le := binary.LittleEndian
	textSize := machoTextSize(len(code))
	dataOff, dataSize := textSize, machoPage
	linkOff := dataOff + dataSize

	got := make([]byte, 8*len(machoImportList))
	for i := range machoImportList {
		v := uint64(i) | 1<<63
		if i < len(machoImportList)-1 {
			v |= 2 << 51 // the next entry is 8 bytes on
		}
		le.PutUint64(got[8*i:], v)
	}
	fixups := machoFixups(dataOff)
	trie := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	strtab := []byte{' ', 0, 0, 0, 0, 0, 0, 0}
	fixupsOff := linkOff
	trieOff := fixupsOff + len(fixups)
	strOff := trieOff + len(trie)
	sigOff := alignUp(strOff+len(strtab), 16)
	sigSize := int(codeSignatureBlobSize(filepath.Base(path), uint64(sigOff)))
	linkSize := sigOff + sigSize - linkOff

	var cmds []byte
	ncmds := 0
	cmd := func(c []byte) {
		cmds = append(cmds, c...)
		ncmds++
	}
	name16 := func(s string) []byte {
		b := make([]byte, 16)
		copy(b, s)
		return b
	}
	segment := func(name string, vmaddr, vmsize, fileoff, filesize int, prot uint32, sects [][]byte) []byte {
		c := make([]byte, 72)
		le.PutUint32(c[0:], 0x19)
		le.PutUint32(c[4:], uint32(72+80*len(sects)))
		copy(c[8:], name16(name))
		le.PutUint64(c[24:], uint64(vmaddr))
		le.PutUint64(c[32:], uint64(vmsize))
		le.PutUint64(c[40:], uint64(fileoff))
		le.PutUint64(c[48:], uint64(filesize))
		le.PutUint32(c[56:], prot)
		le.PutUint32(c[60:], prot)
		le.PutUint32(c[64:], uint32(len(sects)))
		for _, s := range sects {
			c = append(c, s...)
		}
		return c
	}
	sect := func(name, seg string, addr, size, off int, align, flags uint32) []byte {
		s := make([]byte, 80)
		copy(s[0:], name16(name))
		copy(s[16:], name16(seg))
		le.PutUint64(s[32:], uint64(addr))
		le.PutUint64(s[40:], uint64(size))
		le.PutUint32(s[48:], uint32(off))
		le.PutUint32(s[52:], align)
		le.PutUint32(s[64:], flags)
		return s
	}
	linkedit := func(c uint32, off, size int) []byte {
		b := make([]byte, 16)
		le.PutUint32(b[0:], c)
		le.PutUint32(b[4:], 16)
		le.PutUint32(b[8:], uint32(off))
		le.PutUint32(b[12:], uint32(size))
		return b
	}
	withName := func(c uint32, nameOff int, name string, fields func([]byte)) []byte {
		size := alignUp(nameOff+len(name)+1, 8)
		b := make([]byte, size)
		le.PutUint32(b[0:], c)
		le.PutUint32(b[4:], uint32(size))
		le.PutUint32(b[8:], uint32(nameOff))
		copy(b[nameOff:], name)
		if fields != nil {
			fields(b)
		}
		return b
	}

	cmd(segment("__PAGEZERO", 0, machoBase, 0, 0, 0, nil))
	cmd(segment("__TEXT", machoBase, textSize, 0, textSize, 5,
		[][]byte{sect("__text", "__TEXT", machoBase+machoCodeOff, len(code), machoCodeOff, 4, 0x80000400)}))
	cmd(segment("__DATA", machoBase+dataOff, dataSize, dataOff, dataSize, 3,
		[][]byte{sect("__got", "__DATA", machoBase+dataOff, len(got), dataOff, 3, 0x6)}))
	cmd(segment("__LINKEDIT", machoBase+linkOff, alignUp(linkSize, machoPage), linkOff, linkSize, 1, nil))
	cmd(linkedit(0x80000034, fixupsOff, len(fixups))) // LC_DYLD_CHAINED_FIXUPS
	cmd(linkedit(0x80000033, trieOff, 2))             // LC_DYLD_EXPORTS_TRIE
	symtab := make([]byte, 24)
	le.PutUint32(symtab[0:], 0x2)
	le.PutUint32(symtab[4:], 24)
	le.PutUint32(symtab[8:], uint32(strOff))
	le.PutUint32(symtab[16:], uint32(strOff))
	le.PutUint32(symtab[20:], uint32(len(strtab)))
	cmd(symtab)
	dysymtab := make([]byte, 80)
	le.PutUint32(dysymtab[0:], 0xB)
	le.PutUint32(dysymtab[4:], 80)
	cmd(dysymtab)
	cmd(withName(0xE, 12, "/usr/lib/dyld", nil))
	uuid := make([]byte, 24)
	le.PutUint32(uuid[0:], 0x1B)
	le.PutUint32(uuid[4:], 24)
	sum := sha256.Sum256(code)
	copy(uuid[8:], sum[:16])
	uuid[8+6] = uuid[8+6]&0x0F | 0x40
	uuid[8+8] = uuid[8+8]&0x3F | 0x80
	cmd(uuid)
	build := make([]byte, 24)
	le.PutUint32(build[0:], 0x32)
	le.PutUint32(build[4:], 24)
	le.PutUint32(build[8:], 1)           // macOS
	le.PutUint32(build[12:], 0x000B0000) // 11.0
	le.PutUint32(build[16:], 0x000E0000)
	cmd(build)
	mainCmd := make([]byte, 24)
	le.PutUint32(mainCmd[0:], 0x80000028)
	le.PutUint32(mainCmd[4:], 24)
	le.PutUint64(mainCmd[8:], uint64(machoCodeOff+entry))
	cmd(mainCmd)
	cmd(withName(0xC, 24, "/usr/lib/libSystem.B.dylib", func(b []byte) {
		le.PutUint32(b[12:], 2)
		le.PutUint32(b[16:], 0x05276403)
		le.PutUint32(b[20:], 0x00010000)
	}))
	cmd(linkedit(0x1D, sigOff, sigSize)) // LC_CODE_SIGNATURE

	if 32+len(cmds) > machoCodeOff {
		return fmt.Errorf("mach-o load commands too large")
	}
	img := make([]byte, sigOff+sigSize)
	le.PutUint32(img[0:], 0xFEEDFACF)
	le.PutUint32(img[4:], 0x0100000C) // arm64
	le.PutUint32(img[12:], 2)         // MH_EXECUTE
	le.PutUint32(img[16:], uint32(ncmds))
	le.PutUint32(img[20:], uint32(len(cmds)))
	le.PutUint32(img[24:], 0x200085) // NOUNDEFS, DYLDLINK, TWOLEVEL, PIE
	copy(img[32:], cmds)
	copy(img[machoCodeOff:], code)
	copy(img[dataOff:], got)
	copy(img[fixupsOff:], fixups)
	copy(img[trieOff:], trie)
	copy(img[strOff:], strtab)
	sig, err := generateCodeSignature(filepath.Base(path), img[:sigOff], 0, uint64(textSize))
	if err != nil {
		return err
	}
	if len(sig) > sigSize {
		return fmt.Errorf("code signature larger than reserved")
	}
	copy(img[sigOff:], sig)
	if err := os.WriteFile(path, img, 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}
