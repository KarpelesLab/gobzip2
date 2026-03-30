// Package gobzip2 implements reading and writing of bzip2 format compressed data.
//
// This is a pure Go implementation based on the bzip2 1.0.8 reference C code.
// It provides both compression and decompression, unlike the standard library's
// compress/bzip2 package which only supports decompression.
package gobzip2

import "fmt"

// Compression levels.
const (
	BestSpeed          = 1
	DefaultCompression = 9
	BestCompression    = 9
)

// Internal constants matching the bzip2 specification.
const (
	maxAlphaSize  = 258
	maxCodeLen    = 23
	nGroups       = 6
	groupSize     = 50
	nIters        = 4
	maxSelectors  = 2 + (900000 / groupSize) // 18002

	symRUNA = 0
	symRUNB = 1
)

// StructuralError is returned when the bzip2 data is found to be syntactically invalid.
type StructuralError string

func (e StructuralError) Error() string {
	return "bzip2: invalid data: " + string(e)
}

// ChecksumError is returned when the CRC of the decompressed data does not match the expected value.
type ChecksumError struct {
	Expected, Got uint32
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("bzip2: checksum mismatch: expected 0x%08x, got 0x%08x", e.Expected, e.Got)
}
