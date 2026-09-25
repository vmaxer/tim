// Syscall-based printf implementation
// This generates a complete printf runtime function that uses syscalls instead of libc
package main

import (
	"fmt"
	"os"
	"strconv"
)

// GeneratePrintfSyscallRuntime generates syscall-based printf runtime helpers
// Note: The main printf implementation uses compile-time parsing with inline code emission.
// This function generates helper labels that might be used by some code paths.
func (fc *TimCompiler) GeneratePrintfSyscallRuntime() {
	if fc.eb.target.OS() != OSLinux {
		// On non-Linux systems, we still need libc printf
		return
	}

	// Only generate if printf or print_syscall is actually used
	if !fc.usedFunctions["printf"] && !fc.usedFunctions["_tim_print_syscall"] {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "Generating syscall-based printf runtime helpers\n")
	}

	// Generate helper data labels
	fc.eb.Define("_printf_minus", "-")
	fc.eb.Define("_printf_true", "true")
	fc.eb.Define("_printf_false", "false")
	fc.eb.Define("_float_format_str", "%.6f\x00") // Null-terminated for C functions

	// Note: We don't generate runtime printf functions - we use compile-time
	// format string parsing with inline code emission instead.
	// Float formatting is done inline for simplicity and efficiency.
}

// ============================================================================
// UNUSED RUNTIME PRINTF FUNCTIONS (Kept as reference)
// ============================================================================
// The functions below implement a runtime printf with format string parsing.
// They are NOT currently used because the actual implementation uses compile-time
// parsing with inline code emission (see compilePrintfSyscall above).
// These are kept as reference for future work on dynamic printf if needed.
// ============================================================================

// Helper to patch long jumps (32-bit offset)
func (fc *TimCompiler) patchJumpOffset(offsetPos int, targetPos int) {
	bytes := fc.eb.text.Bytes()
	offset := int32(targetPos - (offsetPos + 4))
	bytes[offsetPos] = byte(offset)
	bytes[offsetPos+1] = byte(offset >> 8)
	bytes[offsetPos+2] = byte(offset >> 16)
	bytes[offsetPos+3] = byte(offset >> 24)
}

// compilePrintfSyscall compiles a printf call using inline syscalls
// This is a simplified approach that parses the format string at compile time
// and emits inline syscalls for each segment
func (fc *TimCompiler) compilePrintfSyscall(call *CallExpr, formatStr *StringExpr) {
	processedFormat := formatStr.Value

	// Parse format string and emit inline code for each segment
	argIndex := 0
	i := 0
	runes := []rune(processedFormat)

	for i < len(runes) {
		if runes[i] == '%' && i+1 < len(runes) {
			next := runes[i+1]

			if next == '%' {
				// Escaped %% - print single %
				fc.emitSyscallPrintChar('%')
				i += 2
				continue
			}

			// Check for format with precision (like %.15g)
			precision := 6 // default precision
			if next == '.' {
				// Skip precision specifier - find the actual format character
				i += 2 // skip %.
				precisionStart := i
				for i < len(runes) && (runes[i] >= '0' && runes[i] <= '9') {
					i++
				}
				if i >= len(runes) {
					compilerError("printf: incomplete format specifier")
				}
				if i > precisionStart {
					precisionStr := string(runes[precisionStart:i])
					if p, err := strconv.Atoi(precisionStr); err == nil {
						precision = p
					}
				}
				next = runes[i]
				i++ // we'll increment by 1 below, so total advance is correct
			} else {
				i += 2
			}

			// Format specifier - get the corresponding argument
			if argIndex+1 >= len(call.Args) {
				compilerError("printf: not enough arguments for format string")
			}

			arg := call.Args[argIndex+1] // +1 to skip format string
			argIndex++

			switch next {
			case 'd', 'i', 'l', 'u': // Integer/long/unsigned
				fc.compileExpression(arg)
				fc.emitNumToI64()
				fc.emitSyscallPrintInteger()

			case 'v':
				fc.compileExpression(arg)
				switch t := fc.getExprType(arg); t {
				case "string":
					fc.emitSyscallPrintTimString()
				case "list", "map":
					fc.emitSyscallPrintList(t == "map")
				default:
					fc.emitPrintNumber()
				}

			case 's': // String
				fc.compileExpression(arg)
				// xmm0 contains Tim string pointer - print it
				fc.emitSyscallPrintTimString()

			case 'f', 'g': // Float
				fc.compileExpression(arg)
				fc.emitNumToFloat()
				fc.emitSyscallPrintFloatPrecise(precision)

			case 'p': // Pointer (hex)
				fc.compileExpression(arg)
				fc.emitNumToI64()
				fc.emitSyscallPrintHex()

			case 't', 'b': // Boolean (t=true/false, b=yes/no)
				fc.compileExpression(arg)
				// xmm0 contains value - print "true" or "false"
				if next == 'b' {
					fc.emitSyscallPrintBooleanYesNo()
				} else {
					fc.emitSyscallPrintBoolean()
				}

			default:
				compilerError("printf: unsupported format specifier %%%c", next)
			}
		} else {
			// Regular character or string segment - collect until next %
			start := i
			for i < len(runes) && !(runes[i] == '%' && i+1 < len(runes)) {
				i++
			}

			// Emit this segment as a string literal
			segment := string(runes[start:i])
			if len(segment) > 0 {
				fc.emitSyscallPrintLiteral(segment)
			}
		}
	}
	fc.out.XorpdXmm("xmm0", "xmm0")
}

// emitSyscallPrintLiteral emits code to print a literal string using syscalls
func (fc *TimCompiler) emitSyscallPrintLiteral(str string) {
	labelName := fmt.Sprintf("printf_lit_%d", fc.stringCounter)
	fc.stringCounter++
	fc.eb.Define(labelName, str)

	fc.out.MovImmToReg("rax", "1") // sys_write
	fc.out.MovImmToReg("rdi", "1") // stdout
	fc.out.LeaSymbolToReg("rsi", labelName)
	fc.out.MovImmToReg("rdx", fmt.Sprintf("%d", len(str)))
	fc.out.Syscall()
}

// emitSyscallPrintList prints the values of the list/map whose pointer is in xmm0
// as "[a, b, c]" (or "{a, b, c}" for maps) using write syscalls.
// Layout: [count f64][key0][val0][key1][val1]...
func (fc *TimCompiler) emitSyscallPrintList(isMap bool) {
	open, closing := "[", "]"
	if isMap {
		open, closing = "{", "}"
	}
	fc.out.MovqXmmToReg("rax", "xmm0")
	fc.out.SubImmFromReg("rsp", 64) // [rsp+0..47] float buffer, [rsp+48] ptr, [rsp+56] index
	fc.out.MovRegToMem("rax", "rsp", 48)
	fc.out.XorRegWithReg("rcx", "rcx")
	fc.out.MovRegToMem("rcx", "rsp", 56)
	fc.emitSyscallPrintLiteral(open)

	loop := fc.eb.text.Len()
	fc.out.MovMemToReg("rax", "rsp", 48)
	fc.out.TestRegWithReg("rax", "rax")
	nullDone := fc.eb.text.Len()
	fc.out.JumpConditional(JumpEqual, 0)
	fc.out.MovMemToXmm("xmm0", "rax", 0)
	fc.out.Cvttsd2si("rdx", "xmm0")
	fc.out.MovMemToReg("rcx", "rsp", 56)
	fc.out.CmpRegToReg("rcx", "rdx")
	done := fc.eb.text.Len()
	fc.out.JumpConditional(JumpGreaterOrEqual, 0)

	fc.out.TestRegWithReg("rcx", "rcx")
	first := fc.eb.text.Len()
	fc.out.JumpConditional(JumpEqual, 0)
	fc.emitSyscallPrintLiteral(", ")
	fc.patchJumpOffset(first+2, fc.eb.text.Len())

	fc.out.MovMemToReg("rax", "rsp", 48)
	fc.out.MovMemToReg("rcx", "rsp", 56)
	fc.out.ShlImmReg("rcx", 4)
	fc.out.AddRegToReg("rax", "rcx")
	fc.out.MovMemToXmm("xmm0", "rax", 16)
	fc.emitPrintNumber()

	fc.out.MovMemToReg("rcx", "rsp", 56)
	fc.out.AddImmToReg("rcx", 1)
	fc.out.MovRegToMem("rcx", "rsp", 56)
	fc.out.JumpUnconditional(int32(loop - (fc.eb.text.Len() + 5)))

	fc.patchJumpOffset(done+2, fc.eb.text.Len())
	fc.patchJumpOffset(nullDone+2, fc.eb.text.Len())
	fc.emitSyscallPrintLiteral(closing)
	fc.out.AddImmToReg("rsp", 64)
}

// emitSyscallPrintChar emits code to print a single character
func (fc *TimCompiler) emitSyscallPrintChar(ch rune) {
	fc.out.SubImmFromReg("rsp", 8)
	fc.out.MovImmToReg("rax", fmt.Sprintf("%d", ch))
	fc.out.MovRegToMem("rax", "rsp", 0)
	fc.out.MovImmToReg("rax", "1") // sys_write
	fc.out.MovImmToReg("rdi", "1") // stdout
	fc.out.MovRegToReg("rsi", "rsp")
	fc.out.MovImmToReg("rdx", "1")
	fc.out.Syscall()
	fc.out.AddImmToReg("rsp", 8)
}

// emitSyscallPrintInteger emits code to print an integer in rax
func (fc *TimCompiler) emitSyscallPrintInteger() {
	fc.out.PushReg("rbx")
	fc.out.PushReg("rcx")
	fc.out.PushReg("rdx")

	// Check if negative
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax
	positiveJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x79, 0x00}) // jns (will patch)

	// Negative: print minus, negate, continue
	fc.out.PushReg("rax")
	fc.out.SubImmFromReg("rsp", 8)
	fc.out.MovImmToReg("rax", "45") // '-'
	fc.out.MovRegToMem("rax", "rsp", 0)
	fc.out.MovImmToReg("rax", "1") // sys_write
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovRegToReg("rsi", "rsp")
	fc.out.MovImmToReg("rdx", "1")
	fc.out.Syscall()
	fc.out.AddImmToReg("rsp", 8)
	fc.out.PopReg("rax")
	fc.out.NegReg("rax")

	// Patch positive jump to here
	positiveStart := fc.eb.text.Len()
	fc.eb.text.Bytes()[positiveJump+1] = byte(positiveStart - (positiveJump + 2))

	// Convert to string in buffer
	fc.out.SubImmFromReg("rsp", 32)
	fc.out.LeaMemToReg("rbx", "rsp", 32) // Start at rsp+32, will decrement before writing
	fc.out.MovImmToReg("rcx", "10")

	convertStart := fc.eb.text.Len()
	fc.out.XorRegWithReg("rdx", "rdx")
	fc.out.Emit([]byte{0x48, 0xf7, 0xf1}) // div rcx
	fc.out.AddImmToReg("rdx", 48)
	fc.out.DecReg("rbx")
	fc.out.Emit([]byte{0x88, 0x13})       // mov [rbx], dl
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax
	convertJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x75, 0x00}) // jnz (will patch)
	fc.eb.text.Bytes()[convertJump+1] = byte(convertStart - (convertJump + 2))

	// Calculate length: rsp+32 - rbx
	fc.out.LeaMemToReg("rdx", "rsp", 32)
	fc.out.SubRegFromReg("rdx", "rbx")

	// Write using syscall
	fc.out.MovImmToReg("rax", "1")
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovRegToReg("rsi", "rbx")
	fc.out.Syscall()

	fc.out.AddImmToReg("rsp", 32)
	fc.out.PopReg("rdx")
	fc.out.PopReg("rcx")
	fc.out.PopReg("rbx")
}

// emitSyscallPrintTimString emits code to print a Tim string (in xmm0)
func (fc *TimCompiler) emitSyscallPrintTimString() {
	// Call the existing print syscall helper
	fc.trackFunctionCall("_tim_print_syscall")
	fc.out.MovqXmmToReg("rdi", "xmm0")
	fc.out.CallSymbol("_tim_print_syscall")
}

// emitSyscallPrintBoolean emits code to print true/false based on xmm0
func (fc *TimCompiler) emitSyscallPrintBoolean() {
	// Convert to integer
	fc.out.Cvttsd2si("rax", "xmm0")
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax

	falseJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x74, 0x00}) // je (will patch)

	// Print "true"
	fc.emitSyscallPrintLiteral("true")
	trueJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0xeb, 0x00}) // jmp (will patch)

	// Print "false"
	falseStart := fc.eb.text.Len()
	bytes := fc.eb.text.Bytes()
	bytes[falseJump+1] = byte(falseStart - (falseJump + 2))
	fc.emitSyscallPrintLiteral("false")

	// End
	endStart := fc.eb.text.Len()
	bytes[trueJump+1] = byte(endStart - (trueJump + 2))
}

// emitSyscallPrintBooleanYesNo emits code to print yes/no based on xmm0
func (fc *TimCompiler) emitSyscallPrintBooleanYesNo() {
	// Convert to integer
	fc.out.Cvttsd2si("rax", "xmm0")
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax

	falseJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x74, 0x00}) // je (will patch)

	// Print "yes"
	fc.emitSyscallPrintLiteral("yes")
	trueJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0xeb, 0x00}) // jmp (will patch)

	// Print "no"
	falseStart := fc.eb.text.Len()
	fc.eb.text.Bytes()[falseJump+1] = byte(falseStart - (falseJump + 2))
	fc.emitSyscallPrintLiteral("no")

	// End
	endStart := fc.eb.text.Len()
	fc.eb.text.Bytes()[trueJump+1] = byte(endStart - (trueJump + 2))
}

// emitSyscallPrintFloatPrecise prints a float with 6 decimal places
// Input: xmm0 = float64 value
// FULLY INLINE - zero function calls, direct syscalls only!
func (fc *TimCompiler) emitSyscallPrintFloatPrecise(precision int) {
	// Allocate 160 bytes: 128 for work area + 32 for emitSyscallPrintInteger's stack use
	// This ensures our saved value stays at a consistent offset
	fc.out.SubImmFromReg("rsp", 160)
	if precision < 0 {
		precision = 6
	}
	if precision > 15 {
		precision = 15
	}

	// Save xmm0 at the TOP of our stack frame (offset 152, safe from all modifications)
	fc.out.MovXmmToMem("xmm0", "rsp", 152)

	// Print the sign once and continue with |x|.
	positive := fc.newNumLabel()
	fc.out.MovqXmmToReg("rax", "xmm0")
	fc.out.TestRegWithReg("rax", "rax")
	positive.jcc(JumpGreaterOrEqual)
	fc.out.ShlRegByImm("rax", 1)
	fc.out.ShrRegByImm("rax", 1)
	fc.out.MovRegToMem("rax", "rsp", 152)
	fc.emitSyscallPrintLiteral("-")
	positive.bind()

	// Integer part at [rsp+136], fractional digits (rounded to nearest-even) at
	// [rsp+144], carrying into the integer part when they round up to 10^precision.
	multiplier := 1
	for range precision {
		multiplier *= 10
	}
	fc.out.MovMemToXmm("xmm0", "rsp", 152)
	fc.out.Emit([]byte{0xf2, 0x48, 0x0f, 0x2c, 0xc0}) // cvttsd2si rax, xmm0
	fc.out.MovRegToMem("rax", "rsp", 136)
	fc.out.Emit([]byte{0xf2, 0x48, 0x0f, 0x2a, 0xc8}) // cvtsi2sd xmm1, rax
	fc.out.Emit([]byte{0xf2, 0x0f, 0x5c, 0xc1})       // subsd xmm0, xmm1
	fc.out.MovImmToReg("rax", fmt.Sprintf("%d", multiplier))
	fc.out.Emit([]byte{0xf2, 0x48, 0x0f, 0x2a, 0xc8}) // cvtsi2sd xmm1, rax
	fc.out.Emit([]byte{0xf2, 0x0f, 0x59, 0xc1})       // mulsd xmm0, xmm1
	fc.out.Emit([]byte{0xf2, 0x48, 0x0f, 0x2d, 0xc8}) // cvtsd2si rcx, xmm0
	noCarry := fc.newNumLabel()
	fc.out.CmpRegToReg("rcx", "rax")
	noCarry.jcc(JumpLess)
	fc.out.SubRegFromReg("rcx", "rax")
	fc.out.MovMemToReg("rax", "rsp", 136)
	fc.out.IncReg("rax")
	fc.out.MovRegToMem("rax", "rsp", 136)
	noCarry.bind()
	fc.out.MovRegToMem("rcx", "rsp", 144)

	// ===== Print integer part INLINE (no function calls) =====
	fc.out.MovMemToReg("rax", "rsp", 136)

	// Convert integer to string inline
	fc.out.PushReg("rbx")
	fc.out.PushReg("rcx")
	fc.out.PushReg("rdx")

	// Check if negative
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax
	positiveJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x79, 0x00}) // jns (will patch)

	// Negative: print minus, negate
	fc.out.PushReg("rax")
	fc.out.MovImmToReg("r15", "45") // '-'
	fc.out.MovRegToMem("r15", "rsp", 8)
	fc.out.MovImmToReg("rax", "1")
	fc.out.MovImmToReg("rdi", "1")
	fc.out.LeaMemToReg("rsi", "rsp", 8)
	fc.out.MovImmToReg("rdx", "1")
	fc.out.Syscall()
	fc.out.PopReg("rax")
	fc.out.NegReg("rax")

	// Patch positive jump
	positiveStart := fc.eb.text.Len()
	fc.eb.text.Bytes()[positiveJump+1] = byte(positiveStart - (positiveJump + 2))

	// Convert to string
	fc.out.LeaMemToReg("rbx", "rsp", 32)
	fc.out.MovImmToReg("rcx", "10")

	convertStart := fc.eb.text.Len()
	fc.out.XorRegWithReg("rdx", "rdx")
	fc.out.Emit([]byte{0x48, 0xf7, 0xf1}) // div rcx
	fc.out.AddImmToReg("rdx", 48)
	fc.out.DecReg("rbx")
	fc.out.Emit([]byte{0x88, 0x13})       // mov [rbx], dl
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax
	convertJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x75, 0x00}) // jnz (will patch)
	fc.eb.text.Bytes()[convertJump+1] = byte(convertStart - (convertJump + 2))

	// Calculate length
	fc.out.LeaMemToReg("rdx", "rsp", 32)
	fc.out.SubRegFromReg("rdx", "rbx")

	// Write
	fc.out.MovImmToReg("rax", "1")
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovRegToReg("rsi", "rbx")
	fc.out.Syscall()

	fc.out.PopReg("rdx")
	fc.out.PopReg("rcx")
	fc.out.PopReg("rbx")

	if precision == 0 {
		fc.out.AddImmToReg("rsp", 160)
		return
	}

	// ===== Print decimal point INLINE =====
	fc.out.MovImmToReg("rax", "46") // '.'
	fc.out.MovRegToMem("rax", "rsp", 0)
	fc.out.MovImmToReg("rax", "1")
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovRegToReg("rsi", "rsp")
	fc.out.MovImmToReg("rdx", "1")
	fc.out.Syscall()

	// ===== Fractional digits =====
	fc.out.MovMemToReg("rax", "rsp", 144)

	fc.out.MovImmToReg("rcx", "10")

	for i := precision - 1; i >= 0; i-- {
		fc.out.XorRegWithReg("rdx", "rdx")
		fc.out.Emit([]byte{0x48, 0xf7, 0xf1})
		fc.out.AddImmToReg("rdx", 48)
		fc.out.MovByteRegToMem("dl", "rsp", 64+i)
	}

	// Write
	fc.out.MovImmToReg("rax", "1")
	fc.out.MovImmToReg("rdi", "1")
	fc.out.LeaMemToReg("rsi", "rsp", 64)
	fc.out.MovImmToReg("rdx", fmt.Sprintf("%d", precision))
	fc.out.Syscall()

	// Clean up stack
	fc.out.AddImmToReg("rsp", 160)
}

// emitSyscallPrintHex emits code to print an integer in rax as hex (0x...)
func (fc *TimCompiler) emitSyscallPrintHex() {
	fc.out.PushReg("rbx")
	fc.out.PushReg("rcx")
	fc.out.PushReg("rdx")
	fc.out.PushReg("rax") // Save original value

	// Print "0x"
	fc.emitSyscallPrintLiteral("0x")

	fc.out.PopReg("rax") // Restore value

	// Check if 0
	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax

	// If 0, just print "0"
	zeroJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x75, 0x00}) // jnz (will patch)

	fc.emitSyscallPrintLiteral("0")

	doneJump := fc.eb.text.Len()
	fc.out.JumpUnconditional(0) // jmp (will patch)

	// Not zero
	// Patch zeroJump
	nonZeroStart := fc.eb.text.Len()
	fc.eb.text.Bytes()[zeroJump+1] = byte(nonZeroStart - (zeroJump + 2))

	// Convert to string in buffer (hex)
	fc.out.SubImmFromReg("rsp", 32)
	fc.out.LeaMemToReg("rbx", "rsp", 32) // Start at rsp+32
	fc.out.MovImmToReg("rcx", "16")

	convertStart := fc.eb.text.Len()
	fc.out.XorRegWithReg("rdx", "rdx")
	fc.out.Emit([]byte{0x48, 0xf7, 0xf1}) // div rcx (unsigned divide)
	// remainder in rdx

	// Convert remainder to hex char
	// if rdx < 10: add '0'
	// else: add 'a' - 10

	fc.out.Emit([]byte{0x48, 0x83, 0xfa, 0x0a}) // cmp rdx, 10

	hexCharJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x7c, 0x00}) // jl (will patch) - if < 10

	// >= 10
	fc.out.AddImmToReg("rdx", 87) // 'a' - 10 = 97 - 10 = 87
	// Jump to store
	storeJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0xeb, 0x00}) // jmp (will patch)

	// < 10
	lessThan10Start := fc.eb.text.Len()
	fc.eb.text.Bytes()[hexCharJump+1] = byte(lessThan10Start - (hexCharJump + 2))
	fc.out.AddImmToReg("rdx", 48) // '0'

	// Store
	storeStart := fc.eb.text.Len()
	fc.eb.text.Bytes()[storeJump+1] = byte(storeStart - (storeJump + 2))

	fc.out.DecReg("rbx")
	fc.out.Emit([]byte{0x88, 0x13}) // mov [rbx], dl

	fc.out.Emit([]byte{0x48, 0x85, 0xc0}) // test rax, rax
	convertLoopJump := fc.eb.text.Len()
	fc.out.Emit([]byte{0x75, 0x00}) // jnz (will patch)
	fc.eb.text.Bytes()[convertLoopJump+1] = byte(convertStart - (convertLoopJump + 2))

	// Calculate length: rsp+32 - rbx
	fc.out.LeaMemToReg("rdx", "rsp", 32)
	fc.out.SubRegFromReg("rdx", "rbx")

	// Write using syscall
	fc.out.MovImmToReg("rax", "1")
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovRegToReg("rsi", "rbx")
	fc.out.Syscall()

	fc.out.AddImmToReg("rsp", 32)

	// Patch doneJump
	doneStart := fc.eb.text.Len()
	// jmp unconditional is 5 bytes (E9 xx xx xx xx)
	fc.patchJumpImmediate(doneJump+1, int32(doneStart-(doneJump+5)))

	fc.out.PopReg("rdx")
	fc.out.PopReg("rcx")
	fc.out.PopReg("rbx")
}
