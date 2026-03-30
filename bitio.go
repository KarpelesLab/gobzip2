package gobzip2

import "io"

// bitWriter accumulates bits MSB-first and flushes complete bytes to an io.Writer.
type bitWriter struct {
	w     io.Writer
	buf   uint64
	nbits uint // number of valid bits in buf (counted from MSB)
	err   error
}

func newBitWriter(w io.Writer) *bitWriter {
	return &bitWriter{w: w}
}

// writeBits writes the bottom n bits of v, MSB-first.
func (bw *bitWriter) writeBits(n uint, v uint32) {
	if bw.err != nil {
		return
	}
	// Place bits at the correct position in the 64-bit accumulator.
	bw.buf |= uint64(v) << (64 - bw.nbits - n)
	bw.nbits += n
	for bw.nbits >= 8 {
		b := byte(bw.buf >> 56)
		_, bw.err = bw.w.Write([]byte{b})
		if bw.err != nil {
			return
		}
		bw.buf <<= 8
		bw.nbits -= 8
	}
}

// writeByte writes 8 bits.
func (bw *bitWriter) writeByte(b byte) {
	bw.writeBits(8, uint32(b))
}

// writeUint32 writes a 32-bit value, MSB-first.
func (bw *bitWriter) writeUint32(v uint32) {
	bw.writeBits(8, (v>>24)&0xFF)
	bw.writeBits(8, (v>>16)&0xFF)
	bw.writeBits(8, (v>>8)&0xFF)
	bw.writeBits(8, v&0xFF)
}

// flush pads remaining bits with zeros and writes the final byte.
func (bw *bitWriter) flush() {
	if bw.err != nil {
		return
	}
	for bw.nbits > 0 {
		b := byte(bw.buf >> 56)
		_, bw.err = bw.w.Write([]byte{b})
		if bw.err != nil {
			return
		}
		bw.buf <<= 8
		if bw.nbits >= 8 {
			bw.nbits -= 8
		} else {
			bw.nbits = 0
		}
	}
}

// bitSink is the interface shared by bitWriter and bitBuffer for writing bits.
type bitSink interface {
	writeBits(n uint, v uint32)
	writeByte(b byte)
	writeUint32(v uint32)
}

// bitBuffer accumulates bits in memory without flushing to an io.Writer.
// Used for parallel compression where each block's bit output must be
// collected independently and then serialized in order through a bitWriter.
type bitBuffer struct {
	data  []byte
	buf   uint64
	nbits uint // bits in accumulator
	total uint // total bits written
}

func (bb *bitBuffer) writeBits(n uint, v uint32) {
	bb.buf |= uint64(v) << (64 - bb.nbits - n)
	bb.nbits += n
	bb.total += n
	for bb.nbits >= 8 {
		bb.data = append(bb.data, byte(bb.buf>>56))
		bb.buf <<= 8
		bb.nbits -= 8
	}
}

func (bb *bitBuffer) writeByte(b byte) {
	bb.writeBits(8, uint32(b))
}

func (bb *bitBuffer) writeUint32(v uint32) {
	bb.writeBits(8, (v>>24)&0xFF)
	bb.writeBits(8, (v>>16)&0xFF)
	bb.writeBits(8, (v>>8)&0xFF)
	bb.writeBits(8, v&0xFF)
}

// writeTo writes the exact bits in this buffer to a bitWriter,
// preserving sub-byte alignment (no zero padding).
func (bb *bitBuffer) writeTo(bw *bitWriter) {
	// Write complete bytes
	for _, b := range bb.data {
		bw.writeBits(8, uint32(b))
	}
	// Write remaining bits from accumulator
	if bb.nbits > 0 {
		bw.writeBits(bb.nbits, uint32(bb.buf>>(64-bb.nbits)))
	}
}

// bitReader reads bits MSB-first from an io.ByteReader.
type bitReader struct {
	r     io.ByteReader
	buf   uint64
	nbits uint // number of valid bits in buf (counted from MSB)
	err   error
}

func newBitReader(r io.ByteReader) *bitReader {
	return &bitReader{r: r}
}

// readBits reads n bits and returns them in the bottom n bits of the result.
func (br *bitReader) readBits(n uint) uint32 {
	for br.nbits < n {
		b, err := br.r.ReadByte()
		if err != nil {
			if br.err == nil {
				if err == io.EOF {
					br.err = io.ErrUnexpectedEOF
				} else {
					br.err = err
				}
			}
			return 0
		}
		br.buf |= uint64(b) << (56 - br.nbits)
		br.nbits += 8
	}
	v := uint32(br.buf >> (64 - n))
	br.buf <<= n
	br.nbits -= n
	return v
}

// readBit reads a single bit.
func (br *bitReader) readBit() bool {
	return br.readBits(1) != 0
}

// Err returns the first error encountered during reading.
func (br *bitReader) Err() error {
	return br.err
}
