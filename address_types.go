// address_types.go - Strongly typed addresses to prevent mixing file offsets, virtual addresses, and text offsets
package main

// VirtualAddr represents an address in virtual memory (e.g., 0x403000)
type VirtualAddr uint64

// FileOffset represents an offset in the ELF file (e.g., 0x3000)
type FileOffset uint64

// TextOffset represents an offset within the .text buffer (e.g., 0x9b)
type TextOffset uint64

// RodataOffset represents an offset within the .rodata buffer
type RodataOffset uint64

// DataOffset represents an offset within the .data buffer
type DataOffset uint64

// AddressSpace tracks the mapping between different address spaces
type AddressSpace struct {
	baseAddr     VirtualAddr // Base virtual address (e.g., 0x400000)
	textFileOff  FileOffset  // Where .text starts in file
	textVirtAddr VirtualAddr // Where .text is loaded in memory
}
