package main

// Tiny decompressor stubs for self-extracting executables

// RLE decompressor stub for x86-64 Linux
// Decompresses RIP-relative data to mmap'd memory and jumps to it
func getDecompressorStubLinuxX64() []byte {
	return []byte{
		// lea rsi, [rip + offset]  ; the test patches bytes 3..6 (offset)
		0x48, 0x8D, 0x35, 0x00, 0x00, 0x00, 0x00,

		// mov edi, [rsi]   ; decompressed size (zero-extended into rdi)
		// add rsi, 4       ; skip the size header
		0x8B, 0x3E,
		0x48, 0x83, 0xC6, 0x04,

		// push rbx ; mov rbx, rsi   ; park the compressed pointer
		0x53,
		0x48, 0x89, 0xF3,

		// mmap(NULL, size, PROT_READ|WRITE|EXEC, MAP_PRIVATE|ANON, -1, 0)
		0x48, 0x89, 0xFE, // mov rsi, rdi   ; length = size
		0x48, 0x31, 0xFF, // xor rdi, rdi   ; address = NULL
		0xBA, 0x07, 0x00, 0x00, 0x00, // mov edx, 7
		0x41, 0xBA, 0x22, 0x00, 0x00, 0x00, // mov r10d, 0x22
		0x41, 0xB8, 0xFF, 0xFF, 0xFF, 0xFF, // mov r8d, -1
		0x45, 0x31, 0xC9, // xor r9d, r9d
		0xB8, 0x09, 0x00, 0x00, 0x00, // mov eax, 9 (sys_mmap)
		0x0F, 0x05, // syscall

		// mov r12, rax  ; dest   mov r13, rax  ; entry   mov rsi, rbx  ; src
		0x49, 0x89, 0xC4,
		0x49, 0x89, 0xC5,
		0x48, 0x89, 0xDE,

		// decompress: [count:1][byte:1]..., count 0 ends
		//   movzx ecx, byte [rsi] ; inc rsi ; test cl, cl ; jz done
		0x0F, 0xB6, 0x0E,
		0x48, 0xFF, 0xC6,
		0x84, 0xC9,
		0x74, 0x11, // jz done
		//   movzx eax, byte [rsi] ; inc rsi
		0x0F, 0xB6, 0x06,
		0x48, 0xFF, 0xC6,
		// repeat: mov [r12], al ; inc r12 ; loop repeat
		0x41, 0x88, 0x04, 0x24,
		0x49, 0xFF, 0xC4,
		0xE2, 0xF7, // loop repeat
		0xEB, 0xE5, // jmp decompress_loop

		// done: pop rbx ; jmp r13
		0x5B,
		0x41, 0xFF, 0xE5,
	}
}

// RLE decompressor stub for x86-64 Windows
func getDecompressorStubWindowsX64() []byte {
	// Similar to Linux but uses VirtualAlloc instead of mmap
	// For now, simplified version
	return []byte{
		// TODO: Implement Windows version with VirtualAlloc
		// For now, just return Linux version as placeholder
	}
}

// Simple RLE compression
// Format: [decompressed_size:4][count:1][byte:1]... with count=0 as terminator
func compressRLE(data []byte) []byte {
	if len(data) == 0 {
		return []byte{0, 0, 0, 0, 0} // Empty: size=0, terminator
	}

	result := make([]byte, 0, len(data)/2) // Estimate

	// Add decompressed size (little-endian)
	size := uint32(len(data))
	result = append(result,
		byte(size),
		byte(size>>8),
		byte(size>>16),
		byte(size>>24),
	)

	i := 0
	for i < len(data) {
		b := data[i]
		count := 1

		// Count consecutive identical bytes
		for i+count < len(data) && data[i+count] == b && count < 255 {
			count++
		}

		result = append(result, byte(count), b)
		i += count
	}

	// Terminator
	result = append(result, 0)

	return result
}

// Decompress RLE data (for testing)
func decompressRLE(data []byte) ([]byte, error) {
	if len(data) < 5 {
		return nil, nil
	}

	// Read decompressed size
	size := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	result := make([]byte, 0, size)

	i := 4
	for i < len(data) {
		count := data[i]
		if count == 0 {
			break // Terminator
		}
		i++
		if i >= len(data) {
			break
		}
		b := data[i]
		i++

		for j := 0; j < int(count); j++ {
			result = append(result, b)
		}
	}

	return result, nil
}
