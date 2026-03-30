package gobzip2

// Huffman coding for bzip2.
//
// Implements the three core Huffman operations from the C reference:
//   - makeCodeLengths: build optimal code lengths from symbol frequencies
//   - assignCodes: assign canonical Huffman codes from lengths
//   - createDecodeTables: build lookup tables for decoding

// makeCodeLengths computes Huffman code lengths for the given symbol frequencies.
// The resulting lengths are written into len[0..alphaSize-1].
// No code will exceed maxLen bits.
func makeCodeLengths(lengths []byte, freq []int32, alphaSize, maxLen int) {
	// This is a direct translation of BZ2_hbMakeCodeLengths from huffman.c.
	// Uses a heap-based Huffman tree construction. If any code exceeds maxLen,
	// the frequencies are scaled down and the process is repeated.

	heap := make([]int32, maxAlphaSize+2)
	weight := make([]int32, maxAlphaSize*2)
	parent := make([]int32, maxAlphaSize*2)

	for i := 0; i < alphaSize; i++ {
		f := freq[i]
		if f == 0 {
			f = 1
		}
		weight[i+1] = f << 8
	}

	for {
		nNodes := int32(alphaSize)
		nHeap := int32(0)

		heap[0] = 0
		weight[0] = 0
		parent[0] = -2

		for i := int32(1); i <= int32(alphaSize); i++ {
			parent[i] = -1
			nHeap++
			heap[nHeap] = i
			// upheap
			zz := nHeap
			tmp := heap[zz]
			for weight[tmp] < weight[heap[zz>>1]] {
				heap[zz] = heap[zz>>1]
				zz >>= 1
			}
			heap[zz] = tmp
		}

		for nHeap > 1 {
			// Extract two smallest
			n1 := heap[1]
			heap[1] = heap[nHeap]
			nHeap--
			// downheap
			{
				zz := int32(1)
				tmp := heap[zz]
				for {
					yy := zz << 1
					if yy > nHeap {
						break
					}
					if yy < nHeap && weight[heap[yy+1]] < weight[heap[yy]] {
						yy++
					}
					if weight[tmp] < weight[heap[yy]] {
						break
					}
					heap[zz] = heap[yy]
					zz = yy
				}
				heap[zz] = tmp
			}

			n2 := heap[1]
			heap[1] = heap[nHeap]
			nHeap--
			// downheap
			{
				zz := int32(1)
				tmp := heap[zz]
				for {
					yy := zz << 1
					if yy > nHeap {
						break
					}
					if yy < nHeap && weight[heap[yy+1]] < weight[heap[yy]] {
						yy++
					}
					if weight[tmp] < weight[heap[yy]] {
						break
					}
					heap[zz] = heap[yy]
					zz = yy
				}
				heap[zz] = tmp
			}

			nNodes++
			parent[n1] = nNodes
			parent[n2] = nNodes

			// ADDWEIGHTS: combine weights, depth = 1 + max(depth1, depth2)
			w1, w2 := weight[n1], weight[n2]
			d1, d2 := w1&0xFF, w2&0xFF
			d := d1
			if d2 > d {
				d = d2
			}
			weight[nNodes] = ((w1>>8)+(w2>>8))<<8 | (1 + d)
			parent[nNodes] = -1

			nHeap++
			heap[nHeap] = nNodes
			// upheap
			zz := nHeap
			tmp := heap[zz]
			for weight[tmp] < weight[heap[zz>>1]] {
				heap[zz] = heap[zz>>1]
				zz >>= 1
			}
			heap[zz] = tmp
		}

		tooLong := false
		for i := 1; i <= alphaSize; i++ {
			j := int32(0)
			k := int32(i)
			for parent[k] >= 0 {
				k = parent[k]
				j++
			}
			lengths[i-1] = byte(j)
			if j > int32(maxLen) {
				tooLong = true
			}
		}

		if !tooLong {
			break
		}

		// Scale frequencies down and retry
		for i := 1; i <= alphaSize; i++ {
			j := weight[i] >> 8
			j = 1 + (j / 2)
			weight[i] = j << 8
		}
	}
}

// assignCodes assigns canonical Huffman codes based on the code lengths.
// code[i] receives the Huffman code for symbol i.
func assignCodes(code []int32, length []byte, minLen, maxLen, alphaSize int) {
	vec := int32(0)
	for n := minLen; n <= maxLen; n++ {
		for i := 0; i < alphaSize; i++ {
			if int(length[i]) == n {
				code[i] = vec
				vec++
			}
		}
		vec <<= 1
	}
}

// createDecodeTables builds the limit, base, and perm arrays used for
// fast Huffman decoding. This is a direct translation of BZ2_hbCreateDecodeTables.
func createDecodeTables(limit, base, perm []int32, length []byte, minLen, maxLen, alphaSize int) {
	pp := 0
	for i := minLen; i <= maxLen; i++ {
		for j := 0; j < alphaSize; j++ {
			if int(length[j]) == i {
				perm[pp] = int32(j)
				pp++
			}
		}
	}

	for i := 0; i < maxCodeLen; i++ {
		base[i] = 0
	}
	for i := 0; i < alphaSize; i++ {
		base[length[i]+1]++
	}
	for i := 1; i < maxCodeLen; i++ {
		base[i] += base[i-1]
	}

	for i := 0; i < maxCodeLen; i++ {
		limit[i] = 0
	}
	vec := int32(0)
	for i := minLen; i <= maxLen; i++ {
		vec += base[i+1] - base[i]
		limit[i] = vec - 1
		vec <<= 1
	}
	for i := minLen + 1; i <= maxLen; i++ {
		base[i] = ((limit[i-1] + 1) << 1) - base[i]
	}
}
