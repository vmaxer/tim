package main

import "fmt"

// read_file and write_file on Linux x86-64: system calls on the UTF-8 form
// of their strings. A file that cannot be read or written gives the "io"
// error.

const (
	linuxOpenWrite = 0x241 // O_WRONLY | O_CREAT | O_TRUNC
	linuxFileMode  = 0o644
)

// emitUTF8 compiles e and leaves its UTF-8 bytes in rax: a length word
// followed by the NUL-terminated bytes.
func (fc *TimCompiler) emitUTF8(e Expression) {
	fc.compileExpression(e)
	fc.emitNumCall(valUTF8)
	fc.out.MovqXmmToReg("rax", "xmm0")
}

func (fc *TimCompiler) emitSyscall(nr int) {
	fc.out.MovImmToReg("rax", fmt.Sprint(nr))
	fc.out.Syscall()
}

// emitReadFile reads the whole file at path into a string in xmm0.
func (fc *TimCompiler) emitReadFile(path Expression) {
	fail, failClose, loop, eof, done := fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel()
	fc.emitUTF8(path)
	// locals: [0] fd, [8] size, [16] buffer, [24] bytes read
	fc.out.SubImmFromReg("rsp", 32)
	fc.out.LeaMemToReg("rdi", "rax", 8)
	fc.out.XorRegWithReg("rsi", "rsi")
	fc.out.XorRegWithReg("rdx", "rdx")
	fc.emitSyscall(2) // open
	fc.out.TestRegReg("rax", "rax")
	fail.jcc(JumpLess)
	fc.out.MovRegToMem("rax", "rsp", 0)
	fc.out.MovRegToReg("rdi", "rax")
	fc.out.XorRegWithReg("rsi", "rsi")
	fc.out.MovImmToReg("rdx", "2") // SEEK_END
	fc.emitSyscall(8)              // lseek
	fc.out.TestRegReg("rax", "rax")
	failClose.jcc(JumpLess)
	fc.out.MovRegToMem("rax", "rsp", 8)
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.out.XorRegWithReg("rsi", "rsi")
	fc.out.XorRegWithReg("rdx", "rdx") // SEEK_SET
	fc.emitSyscall(8)
	fc.out.MovMemToReg("rdi", "rsp", 8)
	fc.out.AddImmToReg("rdi", 1)
	fc.callArenaAlloc()
	fc.out.MovRegToMem("rax", "rsp", 16)
	fc.out.MovImmToMem(0, "rsp", 24)
	loop.bind()
	fc.out.MovMemToReg("rdx", "rsp", 8)
	fc.out.MovMemToReg("rcx", "rsp", 24)
	fc.out.SubRegFromReg("rdx", "rcx")
	eof.jcc(JumpEqual)
	fc.out.MovMemToReg("rsi", "rsp", 16)
	fc.out.AddRegToReg("rsi", "rcx")
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.emitSyscall(0) // read
	fc.out.TestRegReg("rax", "rax")
	eof.jcc(JumpLessOrEqual)
	fc.out.MovMemToReg("rcx", "rsp", 24)
	fc.out.AddRegToReg("rcx", "rax")
	fc.out.MovRegToMem("rcx", "rsp", 24)
	loop.jmp()
	eof.bind()
	fc.out.MovMemToReg("rdi", "rsp", 16)
	fc.out.MovMemToReg("rcx", "rsp", 24)
	fc.out.AddRegToReg("rdi", "rcx")
	fc.out.Emit([]byte{0xC6, 0x07, 0x00}) // mov byte [rdi], 0
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.emitSyscall(3) // close
	fc.out.MovMemToReg("rax", "rsp", 16)
	fc.out.MovqRegToXmm("xmm0", "rax")
	fc.emitNumCall(valFromUTF8)
	fc.out.AddImmToReg("rsp", 32)
	done.jmp()
	failClose.bind()
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.emitSyscall(3)
	fail.bind()
	fc.out.AddImmToReg("rsp", 32)
	fc.createErrorResult("io")
	done.bind()
}

// emitWriteFile writes the UTF-8 bytes of content to the file at path,
// replacing it, and leaves the number of bytes written in xmm0.
func (fc *TimCompiler) emitWriteFile(path, content Expression) {
	fail, failClose, loop, finished, done := fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel()
	fc.emitUTF8(content)
	// locals: [0] fd, [8] bytes written, [16] content
	fc.out.SubImmFromReg("rsp", 32)
	fc.out.MovRegToMem("rax", "rsp", 16)
	fc.emitUTF8(path)
	fc.out.LeaMemToReg("rdi", "rax", 8)
	fc.out.MovImmToReg("rsi", fmt.Sprint(linuxOpenWrite))
	fc.out.MovImmToReg("rdx", fmt.Sprint(linuxFileMode))
	fc.emitSyscall(2) // open
	fc.out.TestRegReg("rax", "rax")
	fail.jcc(JumpLess)
	fc.out.MovRegToMem("rax", "rsp", 0)
	fc.out.MovImmToMem(0, "rsp", 8)
	loop.bind()
	fc.out.MovMemToReg("rsi", "rsp", 16)
	fc.out.MovMemToReg("rdx", "rsi", 0)
	fc.out.MovMemToReg("rcx", "rsp", 8)
	fc.out.SubRegFromReg("rdx", "rcx")
	finished.jcc(JumpEqual)
	fc.out.AddImmToReg("rsi", 8)
	fc.out.AddRegToReg("rsi", "rcx")
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.emitSyscall(1) // write
	fc.out.TestRegReg("rax", "rax")
	failClose.jcc(JumpLessOrEqual)
	fc.out.MovMemToReg("rcx", "rsp", 8)
	fc.out.AddRegToReg("rcx", "rax")
	fc.out.MovRegToMem("rcx", "rsp", 8)
	loop.jmp()
	finished.bind()
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.emitSyscall(3) // close
	fc.out.MovMemToReg("rax", "rsp", 8)
	fc.out.AddImmToReg("rsp", 32)
	fc.emitNumFromI64()
	done.jmp()
	failClose.bind()
	fc.out.MovMemToReg("rdi", "rsp", 0)
	fc.emitSyscall(3)
	fail.bind()
	fc.out.AddImmToReg("rsp", 32)
	fc.createErrorResult("io")
	done.bind()
}
