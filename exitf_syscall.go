// exitf syscall implementation for Linux/Windows
package main

import (
	"fmt"
)

// emitStderrWriteLiteral writes a known-length string literal to stderr.
// On Linux uses the write syscall; on Windows uses _write from msvcrt.dll.
func (fc *TimCompiler) emitStderrWriteLiteral(labelName string, length int, isWindows bool) {
	if isWindows {
		fc.out.SubImmFromReg("rsp", 32)
		fc.out.MovImmToReg("rcx", "2")
		fc.out.LeaSymbolToReg("rdx", labelName)
		fc.out.MovImmToReg("r8", fmt.Sprintf("%d", length))
		fc.callFunction("_write", "")
		fc.out.AddImmToReg("rsp", 32)
	} else {
		fc.out.MovImmToReg("rax", "1")
		fc.out.MovImmToReg("rdi", "2")
		fc.out.LeaSymbolToReg("rsi", labelName)
		fc.out.MovImmToReg("rdx", fmt.Sprintf("%d", length))
		fc.out.Syscall()
	}
}

// emitStderrWriteRegBuf writes a buffer whose address is in rsi and length in rdx to stderr.
// On Linux uses the write syscall; on Windows uses _write from msvcrt.dll.
// Caller must have already allocated any needed stack space before calling _tim_itoa.
func (fc *TimCompiler) emitStderrWriteRegBuf(isWindows bool) {
	if isWindows {
		fc.out.MovRegToReg("r8", "rdx")
		fc.out.MovRegToReg("rdx", "rsi")
		fc.out.MovImmToReg("rcx", "2")
		fc.out.SubImmFromReg("rsp", 32)
		fc.callFunction("_write", "")
		fc.out.AddImmToReg("rsp", 32)
	} else {
		fc.out.MovImmToReg("rax", "1")
		fc.out.MovImmToReg("rdi", "2")
		fc.out.Syscall()
	}
}
