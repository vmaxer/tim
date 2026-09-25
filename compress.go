package main

import (
	"bytes"
	"encoding/binary"
)

type Compressor struct {
	windowSize int
	minMatch   int
}

func NewCompressor() *Compressor {
	return &Compressor{
		windowSize: 32768,
		minMatch:   4,
	}
}

func (c *Compressor) Compress(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	var compressed bytes.Buffer

	binary.Write(&compressed, binary.LittleEndian, uint32(len(data)))

	pos := 0
	for pos < len(data) {
		bestLen := 0
		bestDist := 0

		searchStart := max(pos-c.windowSize, 0)

		for i := searchStart; i < pos; i++ {
			matchLen := 0
			for matchLen < 255 && pos+matchLen < len(data) && data[i+matchLen] == data[pos+matchLen] {
				matchLen++
			}

			if matchLen >= c.minMatch && matchLen > bestLen {
				bestLen = matchLen
				bestDist = pos - i
			}
		}

		if bestLen >= c.minMatch {
			compressed.WriteByte(0xFF)
			binary.Write(&compressed, binary.LittleEndian, uint16(bestDist))
			compressed.WriteByte(byte(bestLen))
			pos += bestLen
		} else {
			literal := data[pos]
			if literal == 0xFF {
				compressed.WriteByte(0xFF)
				compressed.WriteByte(0x00)
				compressed.WriteByte(0x00)
				compressed.WriteByte(0x01)
			} else {
				compressed.WriteByte(literal)
			}
			pos++
		}
	}

	return compressed.Bytes()
}

func (c *Compressor) Decompress(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return data, nil
	}

	origSize := binary.LittleEndian.Uint32(data[0:4])
	decompressed := make([]byte, 0, origSize)

	pos := 4
	for pos < len(data) {
		if data[pos] == 0xFF {
			if pos+3 >= len(data) {
				break
			}
			dist := binary.LittleEndian.Uint16(data[pos+1 : pos+3])
			length := int(data[pos+3])

			if dist == 0 && length == 1 {
				decompressed = append(decompressed, 0xFF)
			} else {
				start := len(decompressed) - int(dist)
				for i := range length {
					decompressed = append(decompressed, decompressed[start+i])
				}
			}
			pos += 4
		} else {
			decompressed = append(decompressed, data[pos])
			pos++
		}
	}

	return decompressed, nil
}

// WrapWithDecompressor wraps an ELF executable with compression and decompressor stub
func WrapWithDecompressor(originalELF []byte, arch string) ([]byte, error) {
	if VerboseMode {
		debugf("DEBUG: WrapWithDecompressor called for arch=%s, size=%d\n", arch, len(originalELF))
	}

	// NOTE: Compressing the entire ELF doesn't work because the ELF headers,
	// PLT, GOT, and relocations all assume specific virtual addresses.
	// When we decompress to a different mmap'd address, everything breaks.
	//
	// To properly implement compression, we would need to:
	// 1. Extract only the .text section (machine code)
	// 2. Compress that
	// 3. Have the decompressor write it back to the correct virtual address
	// 4. Or implement position-independent decompression
	//
	// For now, disable compression to avoid segfaults.

	if VerboseMode {
		debugf("DEBUG: Compression disabled for %s (needs position-independent code support)\n", arch)
	}
	return originalELF, nil

	// Keep the old code commented for reference:
	/*
		compressor := NewCompressor()

		// Compress the entire ELF
		compressed := compressor.Compress(originalELF)

		if VerboseMode {
			debugf("DEBUG: Compressed %d -> %d bytes\n", len(originalELF), len(compressed))
		}

		// Generate decompressor stub
		stub := generateDecompressorStub(arch, uint32(len(compressed)), uint32(len(originalELF)))
		if len(stub) == 0 {
			if VerboseMode {
				debugf("DEBUG: No decompressor stub for arch %s\n", arch)
			}
			// Compression not supported for this arch, return original
			return originalELF, nil
		}

	*/

	// Rest of the function is never reached
}
