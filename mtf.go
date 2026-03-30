package gobzip2

// Move-to-front transform for bzip2.
// The MTF transform converts the BWT output into a sequence of small integers
// that are more amenable to Huffman coding.

// mtfEncoder performs the forward MTF transform with RUNA/RUNB zero-run encoding.
type mtfEncoder struct {
	list [256]byte
}

func (m *mtfEncoder) init(nInUse int) {
	for i := 0; i < nInUse; i++ {
		m.list[i] = byte(i)
	}
}

// encode returns the MTF position for the given symbol and moves it to front.
func (m *mtfEncoder) encode(sym byte) int {
	pos := 0
	for m.list[pos] != sym {
		pos++
	}
	// Move to front
	copy(m.list[1:pos+1], m.list[0:pos])
	m.list[0] = sym
	return pos
}

// generateMTFValues performs the forward MTF transform on the BWT output,
// producing MTF values with RUNA/RUNB zero-run encoding.
// Returns the MTF values, frequencies, and total count.
func generateMTFValues(
	ptr []uint32, // sorted suffix indices
	block []byte, // original block data
	nblock int,
	unseqToSeq [256]byte,
	nInUse int,
) (mtfv []uint16, mtfFreq [maxAlphaSize]int32, nMTF int) {
	eob := nInUse + 1
	mtfv = make([]uint16, nblock+1) // +1 for EOB

	var enc mtfEncoder
	enc.init(nInUse)

	wr := 0
	zPend := 0

	for i := 0; i < nblock; i++ {
		j := int(ptr[i]) - 1
		if j < 0 {
			j += nblock
		}
		llI := unseqToSeq[block[j]]

		if enc.list[0] == llI {
			zPend++
		} else {
			if zPend > 0 {
				zPend--
				for {
					if zPend&1 != 0 {
						mtfv[wr] = symRUNB
						wr++
						mtfFreq[symRUNB]++
					} else {
						mtfv[wr] = symRUNA
						wr++
						mtfFreq[symRUNA]++
					}
					if zPend < 2 {
						break
					}
					zPend = (zPend - 2) / 2
				}
				zPend = 0
			}
			// Find symbol position and move to front
			pos := enc.encode(llI)
			mtfv[wr] = uint16(pos + 1)
			wr++
			mtfFreq[pos+1]++
		}
	}

	if zPend > 0 {
		zPend--
		for {
			if zPend&1 != 0 {
				mtfv[wr] = symRUNB
				wr++
				mtfFreq[symRUNB]++
			} else {
				mtfv[wr] = symRUNA
				wr++
				mtfFreq[symRUNA]++
			}
			if zPend < 2 {
				break
			}
			zPend = (zPend - 2) / 2
		}
	}

	mtfv[wr] = uint16(eob)
	wr++
	mtfFreq[eob]++
	nMTF = wr
	return
}

// mtfDecoder is used during decompression to undo the MTF transform.
type mtfDecoder struct {
	list [256]byte
}

func (m *mtfDecoder) init() {
	for i := range m.list {
		m.list[i] = byte(i)
	}
}

// decode returns the symbol at position pos and moves it to front.
func (m *mtfDecoder) decode(pos int) byte {
	sym := m.list[pos]
	copy(m.list[1:pos+1], m.list[0:pos])
	m.list[0] = sym
	return sym
}

// first returns the symbol currently at position 0 (used for RUNA/RUNB runs).
func (m *mtfDecoder) first() byte {
	return m.list[0]
}
