package gobzip2

import (
	"io"
	"math/bits"
)

// Writer compresses data written to it in bzip2 format.
type Writer struct {
	bw  *bitWriter
	err error

	blockSize100k int
	workFactor    int

	// Block accumulation with RLE stage 1
	block     []byte
	nblock    int
	nblockMAX int
	inUse     [256]bool
	blockCRC  uint32

	// RLE state for input
	stateInCh  int // 256 means "no character yet"
	stateInLen int

	// Stream state
	combinedCRC uint32
	blockNo     int

	// Working buffers (reused across blocks)
	ptr  []uint32
	ftab []uint32

	headerWritten bool
}

// NewWriter returns a new Writer that compresses data at the default level (9)
// and writes the compressed output to w.
func NewWriter(w io.Writer) *Writer {
	wr, _ := NewWriterLevel(w, DefaultCompression)
	return wr
}

// NewWriterLevel returns a new Writer with the given block size level (1-9).
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	if level < 1 || level > 9 {
		return nil, StructuralError("invalid block size level")
	}
	n := 100000 * level
	return &Writer{
		bw:            newBitWriter(w),
		blockSize100k: level,
		workFactor:    30,
		block:         make([]byte, n+sortOvershoot),
		nblockMAX:     n - 19,
		ptr:           make([]uint32, n),
		ftab:          make([]uint32, 65537),
		stateInCh:     256,
		blockCRC:      crcInit(),
	}, nil
}

// Write compresses p and writes it to the underlying writer.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n := 0
	for n < len(p) {
		w.addCharToBlock(p[n])
		n++
		if w.nblock >= w.nblockMAX {
			w.err = w.compressBlock(false)
			if w.err != nil {
				return n, w.err
			}
		}
	}
	return n, nil
}

// Close flushes any pending data and writes the bzip2 stream trailer.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	w.flushRL()
	w.err = w.compressBlock(true)
	return w.err
}

// Reset discards internal state and switches to writing to dst.
func (w *Writer) Reset(dst io.Writer) {
	level := w.blockSize100k
	n := 100000 * level
	w.bw = newBitWriter(dst)
	w.err = nil
	w.nblock = 0
	w.blockCRC = crcInit()
	w.stateInCh = 256
	w.stateInLen = 0
	w.combinedCRC = 0
	w.blockNo = 0
	w.headerWritten = false
	w.nblockMAX = n - 19
	for i := range w.inUse {
		w.inUse[i] = false
	}
}

// addCharToBlock implements ADD_CHAR_TO_BLOCK from the C code.
func (w *Writer) addCharToBlock(ch byte) {
	zchh := int(ch)
	if zchh != w.stateInCh && w.stateInLen == 1 {
		// Fast track: previous was a single different char
		c := byte(w.stateInCh)
		w.blockCRC = crcUpdate(w.blockCRC, c)
		w.inUse[w.stateInCh] = true
		w.block[w.nblock] = c
		w.nblock++
		w.stateInCh = zchh
	} else if zchh != w.stateInCh || w.stateInLen == 255 {
		if w.stateInCh < 256 {
			w.addPairToBlock()
		}
		w.stateInCh = zchh
		w.stateInLen = 1
	} else {
		w.stateInLen++
	}
}

func (w *Writer) addPairToBlock() {
	ch := byte(w.stateInCh)
	for i := 0; i < w.stateInLen; i++ {
		w.blockCRC = crcUpdate(w.blockCRC, ch)
	}
	w.inUse[w.stateInCh] = true
	switch w.stateInLen {
	case 1:
		w.block[w.nblock] = ch
		w.nblock++
	case 2:
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = ch
		w.nblock++
	case 3:
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = ch
		w.nblock++
	default:
		w.inUse[w.stateInLen-4] = true
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = ch
		w.nblock++
		w.block[w.nblock] = byte(w.stateInLen - 4)
		w.nblock++
	}
}

func (w *Writer) flushRL() {
	if w.stateInCh < 256 {
		w.addPairToBlock()
	}
	w.stateInCh = 256
	w.stateInLen = 0
}

func (w *Writer) prepareNewBlock() {
	w.nblock = 0
	w.blockCRC = crcInit()
	for i := range w.inUse {
		w.inUse[i] = false
	}
	w.blockNo++
}

func (w *Writer) compressBlock(isLast bool) error {
	bw := w.bw

	// Write stream header if first block
	if !w.headerWritten {
		bw.writeByte('B')
		bw.writeByte('Z')
		bw.writeByte('h')
		bw.writeByte(byte('0' + w.blockSize100k))
		w.headerWritten = true
		w.blockNo = 0
	}

	if w.nblock > 0 {
		blockCRC := crcFinal(w.blockCRC)
		w.combinedCRC = bits.RotateLeft32(w.combinedCRC, 1) ^ blockCRC

		// Block sort
		origPtr := blockSort(w.block, w.ptr, w.ftab, w.nblock, w.workFactor)

		// Write block header
		bw.writeByte(0x31)
		bw.writeByte(0x41)
		bw.writeByte(0x59)
		bw.writeByte(0x26)
		bw.writeByte(0x53)
		bw.writeByte(0x59)

		// Block CRC
		bw.writeUint32(blockCRC)

		// Randomised bit (always 0)
		bw.writeBits(1, 0)

		// origPtr (24 bits)
		bw.writeBits(24, uint32(origPtr))

		// Build unseqToSeq mapping
		var unseqToSeq [256]byte
		nInUse := 0
		for i := 0; i < 256; i++ {
			if w.inUse[i] {
				unseqToSeq[i] = byte(nInUse)
				nInUse++
			}
		}

		// Generate MTF values
		mtfv, mtfFreq, nMTF := generateMTFValues(w.ptr, w.block, w.nblock, unseqToSeq, nInUse)

		// Send MTF values (Huffman coded)
		w.sendMTFValues(bw, mtfv, mtfFreq, nMTF, nInUse)

		if bw.err != nil {
			return bw.err
		}
	}

	// Write stream trailer if last block
	if isLast {
		bw.writeByte(0x17)
		bw.writeByte(0x72)
		bw.writeByte(0x45)
		bw.writeByte(0x38)
		bw.writeByte(0x50)
		bw.writeByte(0x90)
		bw.writeUint32(w.combinedCRC)
		bw.flush()
		return bw.err
	}

	w.prepareNewBlock()
	return bw.err
}

func (w *Writer) sendMTFValues(bw *bitWriter, mtfv []uint16, mtfFreq [maxAlphaSize]int32, nMTF, nInUse int) {
	alphaSize := nInUse + 2

	// Initialize code lengths
	var codeLens [nGroups][maxAlphaSize]byte
	for t := 0; t < nGroups; t++ {
		for v := 0; v < alphaSize; v++ {
			codeLens[t][v] = 15 // BZ_GREATER_ICOST
		}
	}

	// Decide number of groups
	var numGroups int
	switch {
	case nMTF < 200:
		numGroups = 2
	case nMTF < 600:
		numGroups = 3
	case nMTF < 1200:
		numGroups = 4
	case nMTF < 2400:
		numGroups = 5
	default:
		numGroups = 6
	}

	// Initial group assignment
	{
		nPart := numGroups
		remF := nMTF
		gs := 0
		for nPart > 0 {
			tFreq := remF / nPart
			ge := gs - 1
			aFreq := 0
			for aFreq < tFreq && ge < alphaSize-1 {
				ge++
				aFreq += int(mtfFreq[ge])
			}
			if ge > gs && nPart != numGroups && nPart != 1 && ((numGroups-nPart)%2 == 1) {
				aFreq -= int(mtfFreq[ge])
				ge--
			}
			for v := 0; v < alphaSize; v++ {
				if v >= gs && v <= ge {
					codeLens[nPart-1][v] = 0 // BZ_LESSER_ICOST
				} else {
					codeLens[nPart-1][v] = 15 // BZ_GREATER_ICOST
				}
			}
			nPart--
			gs = ge + 1
			remF -= aFreq
		}
	}

	// Iterative refinement
	var selector [maxSelectors]byte
	var rfreq [nGroups][maxAlphaSize]int32
	var code [nGroups][maxAlphaSize]int32

	for iter := 0; iter < nIters; iter++ {
		var fave [nGroups]int
		for t := 0; t < numGroups; t++ {
			for v := 0; v < alphaSize; v++ {
				rfreq[t][v] = 0
			}
		}

		nSelectors := 0
		gs := 0
		for gs < nMTF {
			ge := gs + groupSize - 1
			if ge >= nMTF {
				ge = nMTF - 1
			}

			// Calculate cost for each group
			var cost [nGroups]uint16
			for i := gs; i <= ge; i++ {
				icv := mtfv[i]
				for t := 0; t < numGroups; t++ {
					cost[t] += uint16(codeLens[t][icv])
				}
			}

			// Find best group
			bc := cost[0]
			bt := 0
			for t := 1; t < numGroups; t++ {
				if cost[t] < bc {
					bc = cost[t]
					bt = t
				}
			}
			fave[bt]++
			selector[nSelectors] = byte(bt)
			nSelectors++

			// Update frequencies
			for i := gs; i <= ge; i++ {
				rfreq[bt][mtfv[i]]++
			}
			gs = ge + 1
		}

		// Recompute tables
		for t := 0; t < numGroups; t++ {
			makeCodeLengths(codeLens[t][:alphaSize], rfreq[t][:alphaSize], alphaSize, 17)
		}

		// On last iteration, save selector count
		if iter == nIters-1 {
			w.writeBlockData(bw, &codeLens, &code, selector[:nSelectors], mtfv[:nMTF], numGroups, nSelectors, alphaSize, nInUse)
		}
	}
}

func (w *Writer) writeBlockData(bw *bitWriter, codeLens *[nGroups][maxAlphaSize]byte, code *[nGroups][maxAlphaSize]int32, selector []byte, mtfv []uint16, numGroups, nSelectors, alphaSize, nInUse int) {
	// Compute MTF values for selectors
	selectorMtf := make([]byte, nSelectors)
	{
		var pos [nGroups]byte
		for i := 0; i < numGroups; i++ {
			pos[i] = byte(i)
		}
		for i := 0; i < nSelectors; i++ {
			llI := selector[i]
			j := byte(0)
			tmp := pos[j]
			for llI != tmp {
				j++
				tmp, pos[j] = pos[j], tmp
			}
			pos[0] = tmp
			selectorMtf[i] = j
		}
	}

	// Assign actual codes
	for t := 0; t < numGroups; t++ {
		minLen, maxLen := 32, 0
		for i := 0; i < alphaSize; i++ {
			l := int(codeLens[t][i])
			if l > maxLen {
				maxLen = l
			}
			if l < minLen {
				minLen = l
			}
		}
		assignCodes(code[t][:], codeLens[t][:], minLen, maxLen, alphaSize)
	}

	// Write mapping table
	var inUse16 [16]bool
	for i := 0; i < 16; i++ {
		for j := 0; j < 16; j++ {
			if w.inUse[i*16+j] {
				inUse16[i] = true
			}
		}
	}
	for i := 0; i < 16; i++ {
		if inUse16[i] {
			bw.writeBits(1, 1)
		} else {
			bw.writeBits(1, 0)
		}
	}
	for i := 0; i < 16; i++ {
		if inUse16[i] {
			for j := 0; j < 16; j++ {
				if w.inUse[i*16+j] {
					bw.writeBits(1, 1)
				} else {
					bw.writeBits(1, 0)
				}
			}
		}
	}

	// Write selectors
	bw.writeBits(3, uint32(numGroups))
	bw.writeBits(15, uint32(nSelectors))
	for i := 0; i < nSelectors; i++ {
		for j := byte(0); j < selectorMtf[i]; j++ {
			bw.writeBits(1, 1)
		}
		bw.writeBits(1, 0)
	}

	// Write coding tables
	for t := 0; t < numGroups; t++ {
		curr := int(codeLens[t][0])
		bw.writeBits(5, uint32(curr))
		for i := 0; i < alphaSize; i++ {
			for curr < int(codeLens[t][i]) {
				bw.writeBits(2, 2) // 10 = increment
				curr++
			}
			for curr > int(codeLens[t][i]) {
				bw.writeBits(2, 3) // 11 = decrement
				curr--
			}
			bw.writeBits(1, 0) // 0 = done
		}
	}

	// Write block data
	selCtr := 0
	gs := 0
	for gs < len(mtfv) {
		ge := gs + groupSize - 1
		if ge >= len(mtfv) {
			ge = len(mtfv) - 1
		}
		sel := selector[selCtr]
		for i := gs; i <= ge; i++ {
			sym := mtfv[i]
			bw.writeBits(uint(codeLens[sel][sym]), uint32(code[sel][sym]))
		}
		gs = ge + 1
		selCtr++
	}
}
