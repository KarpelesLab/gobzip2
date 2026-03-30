package gobzip2

import (
	"io"
	"math/bits"
	"runtime"
	"sync"
)

// WriterOptions configures the compressor.
type WriterOptions struct {
	// Level is the block size level (1-9). Default is 9 (900KB blocks).
	Level int

	// Concurrency is the number of goroutines used for compression.
	// 0 or 1 means single-threaded (no goroutine overhead).
	// Higher values enable parallel block compression.
	// Use ParallelCPU for runtime.NumCPU().
	Concurrency int

	// WorkFactor controls when the fallback sorting algorithm kicks in.
	// 0 means use the default (30). Range: 1-250.
	WorkFactor int
}

// ParallelCPU can be used as WriterOptions.Concurrency to use all available CPUs.
const ParallelCPU = -1

// Writer compresses data written to it in bzip2 format.
type Writer struct {
	w   io.Writer
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

	// Working buffers (reused across blocks, single-threaded only)
	ptr  []uint32
	ftab []uint32

	headerWritten bool

	// Parallel compression
	concurrency int
	parallel    *parallelState
}

// NewWriter returns a new Writer that compresses data at the default level (9)
// and writes the compressed output to w.
func NewWriter(w io.Writer) *Writer {
	wr, _ := NewWriterLevel(w, DefaultCompression)
	return wr
}

// NewWriterLevel returns a new Writer with the given block size level (1-9).
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	return NewWriterOptions(w, &WriterOptions{Level: level})
}

// NewWriterOptions returns a new Writer with the given options.
func NewWriterOptions(w io.Writer, opts *WriterOptions) (*Writer, error) {
	level := opts.Level
	if level == 0 {
		level = DefaultCompression
	}
	if level < 1 || level > 9 {
		return nil, StructuralError("invalid block size level")
	}
	workFactor := opts.WorkFactor
	if workFactor == 0 {
		workFactor = 30
	}
	concurrency := opts.Concurrency
	if concurrency == ParallelCPU {
		concurrency = runtime.NumCPU()
	}
	if concurrency < 1 {
		concurrency = 1
	}

	n := 100000 * level
	wr := &Writer{
		w:             w,
		blockSize100k: level,
		workFactor:    workFactor,
		block:         make([]byte, n+sortOvershoot),
		nblockMAX:     n - 19,
		stateInCh:     256,
		blockCRC:      crcInit(),
		concurrency:   concurrency,
	}

	if concurrency <= 1 {
		wr.bw = newBitWriter(w)
		wr.ptr = make([]uint32, n)
		wr.ftab = make([]uint32, 65537)
	} else {
		wr.parallel = newParallelState(w, concurrency, level)
	}

	return wr, nil
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
			w.err = w.flushBlock(false)
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
	w.err = w.flushBlock(true)
	return w.err
}

// Reset discards internal state and switches to writing to dst.
func (w *Writer) Reset(dst io.Writer) {
	level := w.blockSize100k
	n := 100000 * level
	w.w = dst
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
	if w.parallel != nil {
		w.parallel.reset(dst)
	} else {
		w.bw = newBitWriter(dst)
	}
}

// addCharToBlock implements ADD_CHAR_TO_BLOCK from the C code.
func (w *Writer) addCharToBlock(ch byte) {
	zchh := int(ch)
	if zchh != w.stateInCh && w.stateInLen == 1 {
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

// flushBlock dispatches to single-threaded or parallel path.
func (w *Writer) flushBlock(isLast bool) error {
	if w.parallel != nil {
		return w.flushBlockParallel(isLast)
	}
	return w.compressBlockSingle(isLast)
}

// ---- Single-threaded path ----

func (w *Writer) compressBlockSingle(isLast bool) error {
	bw := w.bw

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

		origPtr := blockSort(w.block, w.ptr, w.ftab, w.nblock, w.workFactor)

		bw.writeByte(0x31)
		bw.writeByte(0x41)
		bw.writeByte(0x59)
		bw.writeByte(0x26)
		bw.writeByte(0x53)
		bw.writeByte(0x59)
		bw.writeUint32(blockCRC)
		bw.writeBits(1, 0)
		bw.writeBits(24, uint32(origPtr))

		var unseqToSeq [256]byte
		nInUse := 0
		for i := 0; i < 256; i++ {
			if w.inUse[i] {
				unseqToSeq[i] = byte(nInUse)
				nInUse++
			}
		}

		mtfv, mtfFreq, nMTF := generateMTFValues(w.ptr, w.block, w.nblock, unseqToSeq, nInUse)
		sendMTFValues(bw, w.inUse, mtfv, mtfFreq, nMTF, nInUse)

		if bw.err != nil {
			return bw.err
		}
	}

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

// ---- Parallel path ----

// blockJob represents a block to be compressed by a worker.
type blockJob struct {
	seq      int
	block    []byte // copy of block data
	nblock   int
	inUse    [256]bool
	blockCRC uint32 // finalized CRC
}

// blockResult is the compressed output of a block.
type blockResult struct {
	seq      int
	bits     *bitBuffer // compressed block as exact bit sequence
	blockCRC uint32
}

type parallelState struct {
	w           io.Writer
	level       int
	jobs        chan blockJob
	results     chan blockResult
	done        chan error // serializer completion
	wg          sync.WaitGroup
	serializeMu sync.Mutex
}

func newParallelState(w io.Writer, concurrency, level int) *parallelState {
	ps := &parallelState{
		w:       w,
		level:   level,
		jobs:    make(chan blockJob, concurrency*2),
		results: make(chan blockResult, concurrency*2),
		done:    make(chan error, 1),
	}

	// Start workers
	for i := 0; i < concurrency; i++ {
		ps.wg.Add(1)
		go ps.worker(level)
	}

	// Start serializer
	go ps.serializer(level)

	return ps
}

func (ps *parallelState) worker(level int) {
	defer ps.wg.Done()
	n := 100000 * level
	ptr := make([]uint32, n)
	ftab := make([]uint32, 65537)

	for job := range ps.jobs {
		bits := encodeBlock(job.block, job.nblock, job.inUse, job.blockCRC, ptr, ftab, 30)
		ps.results <- blockResult{
			seq:      job.seq,
			bits:     bits,
			blockCRC: job.blockCRC,
		}
	}
}

func (ps *parallelState) serializer(level int) {
	pending := make(map[int]blockResult)
	nextSeq := 0
	var combinedCRC uint32
	bw := newBitWriter(ps.w)

	// Write stream header
	bw.writeByte('B')
	bw.writeByte('Z')
	bw.writeByte('h')
	bw.writeByte(byte('0' + level))

	var err error
	for res := range ps.results {
		pending[res.seq] = res

		for {
			r, ok := pending[nextSeq]
			if !ok {
				break
			}
			delete(pending, nextSeq)
			nextSeq++

			combinedCRC = bits.RotateLeft32(combinedCRC, 1) ^ r.blockCRC
			r.bits.writeTo(bw)
			if bw.err != nil {
				err = bw.err
				break
			}
		}
		if err != nil {
			break
		}
	}

	if err == nil {
		// Write trailer
		bw.writeByte(0x17)
		bw.writeByte(0x72)
		bw.writeByte(0x45)
		bw.writeByte(0x38)
		bw.writeByte(0x50)
		bw.writeByte(0x90)
		bw.writeUint32(combinedCRC)
		bw.flush()
		err = bw.err
	}

	ps.done <- err
}

func (ps *parallelState) reset(w io.Writer) {
	// Close old channels and wait
	close(ps.jobs)
	ps.wg.Wait()
	close(ps.results)
	<-ps.done

	// Reinitialize
	*ps = *newParallelState(w, cap(ps.jobs)/2, ps.level)
}

func (w *Writer) flushBlockParallel(isLast bool) error {
	ps := w.parallel

	if w.nblock > 0 {
		blockCRC := crcFinal(w.blockCRC)

		// Copy block data for the worker
		blockCopy := make([]byte, w.nblock+sortOvershoot)
		copy(blockCopy, w.block[:w.nblock])

		ps.jobs <- blockJob{
			seq:      w.blockNo,
			block:    blockCopy,
			nblock:   w.nblock,
			inUse:    w.inUse,
			blockCRC: blockCRC,
		}
		w.prepareNewBlock()
	}

	if isLast {
		close(ps.jobs)
		ps.wg.Wait()
		close(ps.results)
		return <-ps.done
	}

	return nil
}

// ---- Standalone block encoder (used by both paths) ----

// encodeBlock compresses a single block into a bitBuffer containing the
// exact bit sequence. The output contains the block header, CRC, origPtr,
// mapping table, selectors, Huffman tables, and compressed data — but NOT
// the stream header or trailer.
func encodeBlock(block []byte, nblock int, inUse [256]bool, blockCRC uint32, ptr, ftab []uint32, workFactor int) *bitBuffer {
	origPtr := blockSort(block, ptr, ftab, nblock, workFactor)

	bb := &bitBuffer{}

	bb.writeByte(0x31)
	bb.writeByte(0x41)
	bb.writeByte(0x59)
	bb.writeByte(0x26)
	bb.writeByte(0x53)
	bb.writeByte(0x59)

	bb.writeUint32(blockCRC)
	bb.writeBits(1, 0) // not randomised
	bb.writeBits(24, uint32(origPtr))

	var unseqToSeq [256]byte
	nInUse := 0
	for i := 0; i < 256; i++ {
		if inUse[i] {
			unseqToSeq[i] = byte(nInUse)
			nInUse++
		}
	}

	mtfv, mtfFreq, nMTF := generateMTFValues(ptr, block, nblock, unseqToSeq, nInUse)
	sendMTFValues(bb, inUse, mtfv, mtfFreq, nMTF, nInUse)

	return bb
}

// ---- Huffman encoding (shared) ----

func sendMTFValues(bw bitSink, inUse [256]bool, mtfv []uint16, mtfFreq [maxAlphaSize]int32, nMTF, nInUse int) {
	alphaSize := nInUse + 2

	var codeLens [nGroups][maxAlphaSize]byte
	for t := 0; t < nGroups; t++ {
		for v := 0; v < alphaSize; v++ {
			codeLens[t][v] = 15
		}
	}

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
					codeLens[nPart-1][v] = 0
				} else {
					codeLens[nPart-1][v] = 15
				}
			}
			nPart--
			gs = ge + 1
			remF -= aFreq
		}
	}

	var selector [maxSelectors]byte
	var rfreq [nGroups][maxAlphaSize]int32
	var code [nGroups][maxAlphaSize]int32

	for iter := 0; iter < nIters; iter++ {
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

			var cost [nGroups]uint16
			for i := gs; i <= ge; i++ {
				icv := mtfv[i]
				for t := 0; t < numGroups; t++ {
					cost[t] += uint16(codeLens[t][icv])
				}
			}

			bc := cost[0]
			bt := 0
			for t := 1; t < numGroups; t++ {
				if cost[t] < bc {
					bc = cost[t]
					bt = t
				}
			}
			selector[nSelectors] = byte(bt)
			nSelectors++

			for i := gs; i <= ge; i++ {
				rfreq[bt][mtfv[i]]++
			}
			gs = ge + 1
		}

		for t := 0; t < numGroups; t++ {
			makeCodeLengths(codeLens[t][:alphaSize], rfreq[t][:alphaSize], alphaSize, 17)
		}

		if iter == nIters-1 {
			writeBlockData(bw, inUse, &codeLens, &code, selector[:nSelectors], mtfv[:nMTF], numGroups, nSelectors, alphaSize, nInUse)
		}
	}
}

func writeBlockData(bw bitSink, inUse [256]bool, codeLens *[nGroups][maxAlphaSize]byte, code *[nGroups][maxAlphaSize]int32, selector []byte, mtfv []uint16, numGroups, nSelectors, alphaSize, nInUse int) {
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

	var inUse16 [16]bool
	for i := 0; i < 16; i++ {
		for j := 0; j < 16; j++ {
			if inUse[i*16+j] {
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
				if inUse[i*16+j] {
					bw.writeBits(1, 1)
				} else {
					bw.writeBits(1, 0)
				}
			}
		}
	}

	bw.writeBits(3, uint32(numGroups))
	bw.writeBits(15, uint32(nSelectors))
	for i := 0; i < nSelectors; i++ {
		for j := byte(0); j < selectorMtf[i]; j++ {
			bw.writeBits(1, 1)
		}
		bw.writeBits(1, 0)
	}

	for t := 0; t < numGroups; t++ {
		curr := int(codeLens[t][0])
		bw.writeBits(5, uint32(curr))
		for i := 0; i < alphaSize; i++ {
			for curr < int(codeLens[t][i]) {
				bw.writeBits(2, 2)
				curr++
			}
			for curr > int(codeLens[t][i]) {
				bw.writeBits(2, 3)
				curr--
			}
			bw.writeBits(1, 0)
		}
	}

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
