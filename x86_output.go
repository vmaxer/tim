package main

// Buffered standard output for Linux x86-64. Writes collect in a 64 KiB
// buffer (mapped on first use) and reach fd 1 when it fills, at every exit,
// before anything else writes (stderr, C code, a forked child) and, when
// stdout is a terminal, at the end of each line.

import "slices"

const outBufferSize = 1 << 16

// emitStdoutWrite writes rdx bytes at rsi to stdout. Clobbers rax, rcx and
// r11 like the write system call it replaces.
func (fc *TimCompiler) emitStdoutWrite() {
	if fc.eb.target.OS() != OSLinux {
		fc.out.MovImmToReg("rax", "1")
		fc.out.MovImmToReg("rdi", "1")
		fc.out.Syscall()
		return
	}
	fc.trackFunctionCall("_tim_out")
	fc.out.CallSymbol("_tim_out")
}

// emitFlushStdout writes out buffered stdout. Clobbers rax, rcx and r11.
func (fc *TimCompiler) emitFlushStdout() {
	if fc.eb.target.OS() != OSLinux {
		return
	}
	fc.trackFunctionCall("_tim_flush")
	fc.out.CallSymbol("_tim_flush")
}

func (fc *TimCompiler) generateOutputBuffer() {
	// state: [0] bytes buffered, [8] mode (0 unset, 1 buffered, 2 terminal),
	// [16] buffer address
	fc.eb.DefineWritable("_tim_out_state", string(make([]byte, 24)))

	fc.eb.MarkLabel("_tim_flush")
	done := fc.newNumLabel()
	fc.out.PushReg("rdi")
	fc.out.PushReg("rsi")
	fc.out.PushReg("rdx")
	fc.out.LeaSymbolToReg("rdi", "_tim_out_state")
	fc.out.MovMemToReg("rdx", "rdi", 0)
	fc.out.TestRegReg("rdx", "rdx")
	done.jcc(JumpEqual)
	fc.out.XorRegWithReg("rax", "rax")
	fc.out.MovRegToMem("rax", "rdi", 0)
	fc.out.MovMemToReg("rsi", "rdi", 16)
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovImmToReg("rax", "1")
	fc.out.Syscall()
	done.bind()
	fc.out.PopReg("rdx")
	fc.out.PopReg("rsi")
	fc.out.PopReg("rdi")
	fc.out.Ret()

	// _tim_out(rsi = bytes, rdx = length)
	fc.eb.MarkLabel("_tim_out")
	saved := []string{"rdi", "rsi", "rdx", "r8", "r9", "r10"}
	for _, r := range saved {
		fc.out.PushReg(r)
	}
	ready, fits, out, buffered := fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel()
	fc.out.LeaSymbolToReg("r8", "_tim_out_state")
	fc.out.MovMemToReg("rax", "r8", 8)
	fc.out.TestRegReg("rax", "rax")
	ready.jcc(JumpNotEqual)
	// First use: map the buffer and check whether stdout is a terminal.
	fc.out.XorRegWithReg("rdi", "rdi")
	fc.out.MovImmToReg("rsi", "65536")
	fc.out.MovImmToReg("rdx", "3")
	fc.out.MovImmToReg("r10", "34")
	fc.out.MovImmToReg("r8", "-1")
	fc.out.XorRegWithReg("r9", "r9")
	fc.out.MovImmToReg("rax", "9")
	fc.out.Syscall()
	fc.out.LeaSymbolToReg("r8", "_tim_out_state")
	fc.out.MovRegToMem("rax", "r8", 16)
	fc.out.SubImmFromReg("rsp", 64)
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovImmToReg("rsi", "21505") // TCGETS
	fc.out.MovRegToReg("rdx", "rsp")
	fc.out.MovImmToReg("rax", "16") // ioctl
	fc.out.Syscall()
	fc.out.AddImmToReg("rsp", 64)
	fc.out.LeaSymbolToReg("r8", "_tim_out_state")
	fc.out.MovImmToReg("rcx", "1")
	fc.out.TestRegReg("rax", "rax")
	buffered.jcc(JumpNotEqual)
	fc.out.MovImmToReg("rcx", "2")
	buffered.bind()
	fc.out.MovRegToMem("rcx", "r8", 8)
	fc.out.MovMemToReg("rsi", "rsp", 32)
	fc.out.MovMemToReg("rdx", "rsp", 24)

	ready.bind()
	fc.out.MovMemToReg("rax", "r8", 0)
	fc.out.LeaMemToReg("rcx", "rax", 0)
	fc.out.AddRegToReg("rcx", "rdx")
	fc.out.CmpRegToImm("rcx", outBufferSize)
	fits.jcc(JumpBelowOrEqual)
	fc.out.CallSymbol("_tim_flush")
	fc.out.XorRegWithReg("rax", "rax")
	fc.out.CmpRegToImm("rdx", outBufferSize)
	fits.jcc(JumpBelow)
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovImmToReg("rax", "1")
	fc.out.Syscall()
	out.jmp()

	fits.bind()
	fc.out.MovMemToReg("rdi", "r8", 16)
	fc.out.AddRegToReg("rdi", "rax")
	fc.out.MovRegToReg("rcx", "rdx")
	fc.out.Emit([]byte{0xF3, 0xA4}) // rep movsb
	fc.out.AddRegToReg("rax", "rdx")
	fc.out.MovRegToMem("rax", "r8", 0)
	fc.out.MovMemToReg("rcx", "r8", 8)
	fc.out.CmpRegToImm("rcx", 2)
	out.jcc(JumpNotEqual)
	fc.out.TestRegReg("rdx", "rdx")
	out.jcc(JumpEqual)
	fc.out.Emit([]byte{0x80, 0x7F, 0xFF, 0x0A}) // cmp byte [rdi-1], '\n'
	out.jcc(JumpNotEqual)
	fc.out.CallSymbol("_tim_flush")
	out.bind()
	for _, s := range slices.Backward(saved) {
		fc.out.PopReg(s)
	}
	fc.out.Ret()
}
