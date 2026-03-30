package gobzip2

import (
	"bufio"
	"io"
	"math/bits"
)

// Reader decompresses bzip2-compressed data read from the underlying reader.
type Reader struct {
	br  *bitReader
	err error

	// Stream state
	blockSize100k int
	combinedCRC   uint32

	// Block output buffer
	tt     []uint32
	outBuf []byte
	outPos int

	// Set to true when we need to read a new block/stream
	needBlock bool
	// Set to true after stream trailer has been read
	streamEnd bool
}

// NewReader returns a new Reader that decompresses bzip2 data from r.
func NewReader(r io.Reader) *Reader {
	br, ok := r.(io.ByteReader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &Reader{
		br:        newBitReader(br),
		needBlock: true,
	}
}

// Read decompresses data into p.
func (r *Reader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if r.err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, r.err
		}

		// Serve from output buffer
		if r.outPos < len(r.outBuf) {
			copied := copy(p[n:], r.outBuf[r.outPos:])
			r.outPos += copied
			n += copied
			continue
		}

		// Need a new block
		if r.needBlock {
			r.err = r.readNext()
			continue
		}

		// Should not reach here
		r.needBlock = true
	}
	return n, nil
}

// Close releases resources. It does not close the underlying reader.
func (r *Reader) Close() error {
	return nil
}

// Reset discards internal state and switches to reading from src.
func (r *Reader) Reset(src io.Reader) {
	br, ok := src.(io.ByteReader)
	if !ok {
		br = bufio.NewReader(src)
	}
	*r = Reader{
		br:        newBitReader(br),
		needBlock: true,
		tt:        r.tt,
		outBuf:    r.outBuf,
	}
}

// readNext reads the next block or stream header and decodes it fully into outBuf.
func (r *Reader) readNext() error {
	if r.blockSize100k == 0 || r.streamEnd {
		// Need to read a stream header
		err := r.readStreamHeader()
		if err != nil {
			return err
		}
	}
	return r.readBlock()
}

func (r *Reader) readStreamHeader() error {
	br := r.br

	b := br.readBits(8)
	if br.Err() != nil {
		if r.combinedCRC != 0 || r.streamEnd {
			// Already read at least one complete stream; EOF here is normal
			return io.EOF
		}
		return br.Err()
	}
	z := br.readBits(8)
	h := br.readBits(8)
	if br.Err() != nil {
		return br.Err()
	}

	if b != 'B' || z != 'Z' || h != 'h' {
		return StructuralError("bad magic value")
	}

	level := br.readBits(8)
	if br.Err() != nil {
		return br.Err()
	}
	if level < '1' || level > '9' {
		return StructuralError("bad block size")
	}

	r.blockSize100k = int(level - '0')
	r.combinedCRC = 0
	r.streamEnd = false

	maxBlock := r.blockSize100k * 100000
	if cap(r.tt) < maxBlock {
		r.tt = make([]uint32, maxBlock)
	} else {
		r.tt = r.tt[:maxBlock]
	}

	return nil
}

func (r *Reader) readBlock() error {
	br := r.br

	// Read 6-byte magic
	magic := [6]byte{
		byte(br.readBits(8)),
		byte(br.readBits(8)),
		byte(br.readBits(8)),
		byte(br.readBits(8)),
		byte(br.readBits(8)),
		byte(br.readBits(8)),
	}
	if br.Err() != nil {
		return br.Err()
	}

	// Check for end-of-stream marker: 0x17 0x72 0x45 0x38 0x50 0x90
	if magic == [6]byte{0x17, 0x72, 0x45, 0x38, 0x50, 0x90} {
		storedCombinedCRC := br.readBits(32)
		if br.Err() != nil {
			return br.Err()
		}
		if storedCombinedCRC != r.combinedCRC {
			return &ChecksumError{Expected: storedCombinedCRC, Got: r.combinedCRC}
		}
		r.streamEnd = true
		// Try to read another concatenated stream
		return r.readNext()
	}

	// Check for block header: 0x31 0x41 0x59 0x26 0x53 0x59
	if magic != [6]byte{0x31, 0x41, 0x59, 0x26, 0x53, 0x59} {
		return StructuralError("bad block magic")
	}

	storedBlockCRC := br.readBits(32)
	blockRandomised := br.readBit()
	origPtr := int(br.readBits(24))
	if br.Err() != nil {
		return br.Err()
	}

	// Read byte usage bitmap
	var inUse [256]bool
	inUse16 := br.readBits(16)
	for i := 0; i < 16; i++ {
		if inUse16&(1<<uint(15-i)) != 0 {
			bits16 := br.readBits(16)
			for j := 0; j < 16; j++ {
				if bits16&(1<<uint(15-j)) != 0 {
					inUse[i*16+j] = true
				}
			}
		}
	}
	if br.Err() != nil {
		return br.Err()
	}

	var seqToUnseq [256]byte
	nInUse := 0
	for i := 0; i < 256; i++ {
		if inUse[i] {
			seqToUnseq[nInUse] = byte(i)
			nInUse++
		}
	}
	if nInUse == 0 {
		return StructuralError("no symbols in block")
	}
	alphaSize := nInUse + 2

	// Read selectors
	numGroups := int(br.readBits(3))
	if numGroups < 2 || numGroups > nGroups {
		return StructuralError("bad number of Huffman groups")
	}
	numSelectors := int(br.readBits(15))
	if numSelectors < 1 {
		return StructuralError("bad number of selectors")
	}

	selectorMtf := make([]byte, numSelectors)
	for i := 0; i < numSelectors; i++ {
		j := 0
		for br.readBit() {
			j++
			if j >= numGroups {
				return StructuralError("bad selector")
			}
		}
		selectorMtf[i] = byte(j)
	}
	if numSelectors > maxSelectors {
		numSelectors = maxSelectors
		selectorMtf = selectorMtf[:maxSelectors]
	}
	if br.Err() != nil {
		return br.Err()
	}

	// Undo MTF on selectors
	selector := make([]byte, numSelectors)
	{
		var pos [nGroups]byte
		for i := 0; i < numGroups; i++ {
			pos[i] = byte(i)
		}
		for i := 0; i < numSelectors; i++ {
			v := selectorMtf[i]
			tmp := pos[v]
			for v > 0 {
				pos[v] = pos[v-1]
				v--
			}
			pos[0] = tmp
			selector[i] = tmp
		}
	}

	// Read Huffman tables
	var codeLens [nGroups][maxAlphaSize]byte
	for t := 0; t < numGroups; t++ {
		curr := int(br.readBits(5))
		for i := 0; i < alphaSize; i++ {
			for {
				if curr < 1 || curr > 20 {
					return StructuralError("bad Huffman code length")
				}
				if !br.readBit() {
					break
				}
				if br.readBit() {
					curr--
				} else {
					curr++
				}
			}
			codeLens[t][i] = byte(curr)
		}
	}
	if br.Err() != nil {
		return br.Err()
	}

	// Create decode tables
	var (
		limit   [nGroups][maxAlphaSize]int32
		base    [nGroups][maxAlphaSize]int32
		perm    [nGroups][maxAlphaSize]int32
		minLens [nGroups]int
	)
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
		createDecodeTables(limit[t][:], base[t][:], perm[t][:], codeLens[t][:], minLen, maxLen, alphaSize)
		minLens[t] = minLen
	}

	// Decode MTF values
	eob := nInUse + 1
	nblockMAX := r.blockSize100k * 100000
	groupNo := -1
	groupPos := 0
	var gLimit, gBase, gPerm []int32
	var gMinlen int

	var unzftab [256]int32
	var mtfDec mtfDecoder
	mtfDec.init()

	nblock := 0

	getSymbol := func() (int, error) {
		if groupPos == 0 {
			groupNo++
			if groupNo >= numSelectors {
				return 0, StructuralError("ran out of selectors")
			}
			groupPos = groupSize
			gSel := int(selector[groupNo])
			gMinlen = minLens[gSel]
			gLimit = limit[gSel][:]
			gPerm = perm[gSel][:]
			gBase = base[gSel][:]
		}
		groupPos--

		zn := gMinlen
		zvec := int32(br.readBits(uint(zn)))
		for {
			if zn > 20 {
				return 0, StructuralError("Huffman code too long")
			}
			if zvec <= gLimit[zn] {
				break
			}
			zn++
			zvec = (zvec << 1) | int32(br.readBits(1))
		}
		if br.Err() != nil {
			return 0, br.Err()
		}
		idx := zvec - gBase[zn]
		if idx < 0 || idx >= int32(maxAlphaSize) {
			return 0, StructuralError("bad Huffman code")
		}
		return int(gPerm[idx]), nil
	}

	nextSym, err := getSymbol()
	if err != nil {
		return err
	}

	for {
		if nextSym == eob {
			break
		}

		if nextSym == symRUNA || nextSym == symRUNB {
			es := -1
			N := 1
			for {
				if N >= 2*1024*1024 {
					return StructuralError("RUNA/RUNB overflow")
				}
				if nextSym == symRUNA {
					es += 1 * N
				} else {
					es += 2 * N
				}
				N *= 2
				nextSym, err = getSymbol()
				if err != nil {
					return err
				}
				if nextSym != symRUNA && nextSym != symRUNB {
					break
				}
			}
			es++

			uc := seqToUnseq[mtfDec.first()]
			unzftab[uc] += int32(es)
			for es > 0 {
				if nblock >= nblockMAX {
					return StructuralError("block overflow")
				}
				r.tt[nblock] = uint32(uc)
				nblock++
				es--
			}
			continue
		}

		if nblock >= nblockMAX {
			return StructuralError("block overflow")
		}
		uc := seqToUnseq[mtfDec.decode(nextSym-1)]
		unzftab[uc]++
		r.tt[nblock] = uint32(uc)
		nblock++

		nextSym, err = getSymbol()
		if err != nil {
			return err
		}
	}

	if origPtr < 0 || origPtr >= nblock {
		return StructuralError("bad origPtr")
	}

	// Build cftab
	var cftab [257]int32
	for i := 0; i < 256; i++ {
		cftab[i+1] = unzftab[i]
	}
	for i := 1; i <= 256; i++ {
		cftab[i] += cftab[i-1]
	}
	if cftab[256] != int32(nblock) {
		return StructuralError("cftab inconsistency")
	}

	// Compute T^(-1)
	{
		var c [257]int32
		copy(c[:], cftab[:])
		for i := 0; i < nblock; i++ {
			uc := byte(r.tt[i] & 0xff)
			r.tt[c[uc]] |= uint32(i) << 8
			c[uc]++
		}
	}

	// Walk the inverse BWT chain and RLE-decode into outBuf.
	// This follows the C code's unRLE_obuf_to_output_FAST logic exactly.
	r.outBuf = r.outBuf[:0]
	blockCRC := crcInit()

	tPos := r.tt[origPtr] >> 8
	nblockUsed := 0

	// Randomization state
	var rNToGo, rTPos int32

	getByte := func() byte {
		b := byte(r.tt[tPos] & 0xff)
		tPos = r.tt[tPos] >> 8
		nblockUsed++

		if blockRandomised {
			if rNToGo == 0 {
				rNToGo = randNums[rTPos]
				rTPos++
				if rTPos == 512 {
					rTPos = 0
				}
			}
			rNToGo--
			if rNToGo == 0 {
				b ^= 1
			}
		}
		return b
	}

	emit := func(b byte, count int) {
		for i := 0; i < count; i++ {
			blockCRC = crcUpdate(blockCRC, b)
		}
		for i := 0; i < count; i++ {
			r.outBuf = append(r.outBuf, b)
		}
	}

	// State machine matching the C code.
	// nblock is the number of entries in the chain. We call getByte() exactly nblock times.
	if nblock > 0 {
		k0 := getByte() // consumes 1; nblockUsed == 1 after this

		for {
			// End of block?
			if nblockUsed == nblock {
				emit(k0, 1)
				break
			}

			// Get next byte
			k1 := getByte()
			if k1 != k0 {
				emit(k0, 1)
				k0 = k1
				continue
			}

			// 2nd identical byte
			if nblockUsed == nblock {
				emit(k0, 2)
				break
			}
			k1 = getByte()
			if k1 != k0 {
				emit(k0, 2)
				k0 = k1
				continue
			}

			// 3rd identical byte
			if nblockUsed == nblock {
				emit(k0, 3)
				break
			}
			k1 = getByte()
			if k1 != k0 {
				emit(k0, 3)
				k0 = k1
				continue
			}

			// 4th identical byte - next is repeat count
			if nblockUsed == nblock {
				emit(k0, 4)
				break
			}
			repCount := int(getByte())
			emit(k0, repCount+4)

			if nblockUsed == nblock {
				break
			}
			k0 = getByte()
		}
	}

	blockCRC = crcFinal(blockCRC)
	if blockCRC != storedBlockCRC {
		return &ChecksumError{Expected: storedBlockCRC, Got: blockCRC}
	}

	r.combinedCRC = bits.RotateLeft32(r.combinedCRC, 1) ^ blockCRC
	r.outPos = 0
	r.needBlock = false
	return nil
}
