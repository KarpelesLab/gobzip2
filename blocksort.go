package gobzip2

// Block sorting for bzip2 compression.
// Implements the Burrows-Wheeler Transform (BWT) forward transform.
//
// Two sorting algorithms are provided:
//   - mainSort: fast O(N) average for normal data, uses 2-byte radix sort + quicksort
//   - fallbackSort: O(N log^2 N) for repetitive data, uses exponential radix refinement
//
// This is a faithful translation of blocksort.c from bzip2 1.0.8.

const (
	sortRadix     = 2  // BZ_N_RADIX
	sortQSort     = 12 // BZ_N_QSORT
	sortShell     = 18 // BZ_N_SHELL
	sortOvershoot = sortRadix + sortQSort + sortShell + 2

	mainQSortSmallThresh = 20
	mainQSortDepthThresh = sortRadix + sortQSort
	mainQSortStackSize   = 100

	fallbackQSortSmallThresh = 10
	fallbackQSortStackSize   = 100

	setMask   = 1 << 21
	clearMask = ^uint32(setMask)
)

// Shell sort increments (Knuth's sequence)
var shellIncs = [14]int{1, 4, 13, 40, 121, 364, 1093, 3280,
	9841, 29524, 88573, 265720, 797161, 2391484}

// blockSort performs the BWT on the block data, producing a sorted suffix array.
// Returns origPtr (the position of the original first rotation in sorted order).
func blockSort(block []byte, ptr []uint32, ftab []uint32, nblock int, workFactor int) int {
	if nblock < 10000 {
		fallbackSort(ptr, block, ftab, nblock)
	} else {
		if workFactor < 1 {
			workFactor = 1
		}
		if workFactor > 100 {
			workFactor = 100
		}
		budget := nblock * ((workFactor - 1) / 3)
		mainSortFull(ptr, block, ftab, nblock, &budget)
		if budget < 0 {
			fallbackSort(ptr, block, ftab, nblock)
		}
	}

	// Find origPtr
	for i := 0; i < nblock; i++ {
		if ptr[i] == 0 {
			return i
		}
	}
	panic("blockSort: origPtr not found")
}

// ---- Fallback sort ----

func fallbackSimpleSort(fmap, eclass []uint32, lo, hi int) {
	if lo == hi {
		return
	}
	if hi-lo > 3 {
		for i := hi - 4; i >= lo; i-- {
			tmp := fmap[i]
			ecTmp := eclass[tmp]
			j := i + 4
			for j <= hi && ecTmp > eclass[fmap[j]] {
				fmap[j-4] = fmap[j]
				j += 4
			}
			fmap[j-4] = tmp
		}
	}
	for i := hi - 1; i >= lo; i-- {
		tmp := fmap[i]
		ecTmp := eclass[tmp]
		j := i + 1
		for j <= hi && ecTmp > eclass[fmap[j]] {
			fmap[j-1] = fmap[j]
			j++
		}
		fmap[j-1] = tmp
	}
}

func fallbackQSort3(fmap, eclass []uint32, loSt, hiSt int) {
	var stackLo, stackHi [fallbackQSortStackSize]int
	sp := 0
	r := uint32(0)

	stackLo[sp] = loSt
	stackHi[sp] = hiSt
	sp++

	for sp > 0 {
		sp--
		lo := stackLo[sp]
		hi := stackHi[sp]

		if hi-lo < fallbackQSortSmallThresh {
			fallbackSimpleSort(fmap, eclass, lo, hi)
			continue
		}

		r = ((r * 7621) + 1) % 32768
		r3 := r % 3
		var med uint32
		switch r3 {
		case 0:
			med = eclass[fmap[lo]]
		case 1:
			med = eclass[fmap[(lo+hi)>>1]]
		default:
			med = eclass[fmap[hi]]
		}

		unLo, ltLo := lo, lo
		unHi, gtHi := hi, hi

		for {
			for {
				if unLo > unHi {
					break
				}
				n := int64(eclass[fmap[unLo]]) - int64(med)
				if n == 0 {
					fmap[unLo], fmap[ltLo] = fmap[ltLo], fmap[unLo]
					ltLo++
					unLo++
					continue
				}
				if n > 0 {
					break
				}
				unLo++
			}
			for {
				if unLo > unHi {
					break
				}
				n := int64(eclass[fmap[unHi]]) - int64(med)
				if n == 0 {
					fmap[unHi], fmap[gtHi] = fmap[gtHi], fmap[unHi]
					gtHi--
					unHi--
					continue
				}
				if n < 0 {
					break
				}
				unHi--
			}
			if unLo > unHi {
				break
			}
			fmap[unLo], fmap[unHi] = fmap[unHi], fmap[unLo]
			unLo++
			unHi--
		}

		if gtHi < ltLo {
			continue
		}

		n := ltLo - lo
		if m := unLo - ltLo; m < n {
			n = m
		}
		vecSwapU32(fmap, lo, unLo-n, n)

		m := hi - gtHi
		if m2 := gtHi - unHi; m2 < m {
			m = m2
		}
		vecSwapU32(fmap, unLo, hi-m+1, m)

		n = lo + unLo - ltLo - 1
		m = hi - (gtHi - unHi) + 1

		if n-lo > hi-m {
			stackLo[sp] = lo
			stackHi[sp] = n
			sp++
			stackLo[sp] = m
			stackHi[sp] = hi
			sp++
		} else {
			stackLo[sp] = m
			stackHi[sp] = hi
			sp++
			stackLo[sp] = lo
			stackHi[sp] = n
			sp++
		}
	}
}

func vecSwapU32(s []uint32, a, b, n int) {
	for n > 0 {
		s[a], s[b] = s[b], s[a]
		a++
		b++
		n--
	}
}

func fallbackSort(fmap []uint32, eclass8Block []byte, ftabWork []uint32, nblock int) {
	// eclass shares memory with the block in the C code. In Go, we use
	// a separate allocation for eclass since we can't alias []byte as []uint32.
	eclass := make([]uint32, nblock)

	// Use ftabWork as bhtab. Need enough words to cover nblock + 64 sentinel bits.
	nBhtab := 2 + (nblock+64)/32
	if nBhtab > len(ftabWork) {
		nBhtab = len(ftabWork)
	}
	bhtab := ftabWork[:nBhtab]

	setBH := func(i int) { bhtab[i>>5] |= 1 << uint(i&31) }
	clearBH := func(i int) { bhtab[i>>5] &^= 1 << uint(i&31) }
	issetBH := func(i int) bool { return bhtab[i>>5]&(1<<uint(i&31)) != 0 }
	wordBH := func(i int) uint32 { return bhtab[i>>5] }

	// Initial 1-char radix sort
	var ftab [257]int32
	var ftabCopy [256]int32
	for i := 0; i < nblock; i++ {
		ftab[eclass8Block[i]]++
	}
	for i := 0; i < 256; i++ {
		ftabCopy[i] = ftab[i]
	}
	for i := 1; i < 257; i++ {
		ftab[i] += ftab[i-1]
	}
	for i := 0; i < nblock; i++ {
		j := eclass8Block[i]
		k := ftab[j] - 1
		ftab[j] = k
		fmap[k] = uint32(i)
	}

	for i := 0; i < nBhtab; i++ {
		bhtab[i] = 0
	}
	for i := 0; i < 256; i++ {
		setBH(int(ftab[i]))
	}

	// Sentinel bits
	for i := 0; i < 32; i++ {
		setBH(nblock + 2*i)
		clearBH(nblock + 2*i + 1)
	}

	// Exponential radix refinement (Manber-Myers inspired)
	H := 1
	for {
		j := 0
		for i := 0; i < nblock; i++ {
			if issetBH(i) {
				j = i
			}
			k := int(fmap[i]) - H
			if k < 0 {
				k += nblock
			}
			eclass[k] = uint32(j)
		}

		nNotDone := 0
		r := -1
		for {
			// Find next non-singleton bucket
			k := r + 1
			for issetBH(k) && (k&0x1f) != 0 {
				k++
			}
			if issetBH(k) {
				for wordBH(k) == 0xffffffff {
					k += 32
				}
				for issetBH(k) {
					k++
				}
			}
			l := k - 1
			if l >= nblock {
				break
			}
			for !issetBH(k) && (k&0x1f) != 0 {
				k++
			}
			if !issetBH(k) {
				for wordBH(k) == 0 {
					k += 32
				}
				for !issetBH(k) {
					k++
				}
			}
			r = k - 1
			if r >= nblock {
				break
			}

			if r > l {
				nNotDone += (r - l + 1)
				fallbackQSort3(fmap, eclass, l, r)

				cc := int32(-1)
				for i := l; i <= r; i++ {
					cc1 := int32(eclass[fmap[i]])
					if cc != cc1 {
						setBH(i)
						cc = cc1
					}
				}
			}
		}

		H *= 2
		if H > nblock || nNotDone == 0 {
			break
		}
	}

	// Reconstruct block in eclass8Block
	j := 0
	for i := 0; i < nblock; i++ {
		for ftabCopy[j] == 0 {
			j++
		}
		ftabCopy[j]--
		eclass8Block[fmap[i]] = byte(j)
	}
}

// ---- Main sort ----

func mainGtU(i1, i2 uint32, block []byte, quadrant []uint16, nblock uint32, budget *int) bool {
	// Compare rotations starting at i1 and i2
	// First 12 bytes compared without quadrant
	for k := 0; k < 12; k++ {
		c1 := block[i1]
		c2 := block[i2]
		if c1 != c2 {
			return c1 > c2
		}
		i1++
		i2++
	}

	// Remaining comparison uses quadrant for tiebreaking
	k := int(nblock) + 8
	for k >= 0 {
		c1 := block[i1]
		c2 := block[i2]
		if c1 != c2 {
			return c1 > c2
		}
		s1 := quadrant[i1]
		s2 := quadrant[i2]
		if s1 != s2 {
			return s1 > s2
		}
		i1++
		i2++

		c1 = block[i1]
		c2 = block[i2]
		if c1 != c2 {
			return c1 > c2
		}
		s1 = quadrant[i1]
		s2 = quadrant[i2]
		if s1 != s2 {
			return s1 > s2
		}
		i1++
		i2++

		c1 = block[i1]
		c2 = block[i2]
		if c1 != c2 {
			return c1 > c2
		}
		s1 = quadrant[i1]
		s2 = quadrant[i2]
		if s1 != s2 {
			return s1 > s2
		}
		i1++
		i2++

		c1 = block[i1]
		c2 = block[i2]
		if c1 != c2 {
			return c1 > c2
		}
		s1 = quadrant[i1]
		s2 = quadrant[i2]
		if s1 != s2 {
			return s1 > s2
		}
		i1++
		i2++

		if i1 >= nblock {
			i1 -= nblock
		}
		if i2 >= nblock {
			i2 -= nblock
		}
		k -= 4
		*budget--
	}
	return false
}

func mmed3(a, b, c byte) byte {
	if a > b {
		a, b = b, a
	}
	if b > c {
		b = c
		if a > b {
			b = a
		}
	}
	return b
}

func mainSimpleSort(ptr []uint32, block []byte, quadrant []uint16, nblock uint32, lo, hi, d int, budget *int) {
	bigN := hi - lo + 1
	if bigN < 2 {
		return
	}

	hp := 0
	for shellIncs[hp] < bigN {
		hp++
	}
	hp--

	for ; hp >= 0; hp-- {
		h := shellIncs[hp]
		i := lo + h
		for i <= hi {
			v := ptr[i]
			j := i
			for mainGtU(ptr[j-h]+uint32(d), v+uint32(d), block, quadrant, nblock, budget) {
				ptr[j] = ptr[j-h]
				j -= h
				if j <= lo+h-1 {
					break
				}
			}
			ptr[j] = v

			i++
			if i > hi {
				break
			}
			v = ptr[i]
			j = i
			for mainGtU(ptr[j-h]+uint32(d), v+uint32(d), block, quadrant, nblock, budget) {
				ptr[j] = ptr[j-h]
				j -= h
				if j <= lo+h-1 {
					break
				}
			}
			ptr[j] = v

			i++
			if i > hi {
				break
			}
			v = ptr[i]
			j = i
			for mainGtU(ptr[j-h]+uint32(d), v+uint32(d), block, quadrant, nblock, budget) {
				ptr[j] = ptr[j-h]
				j -= h
				if j <= lo+h-1 {
					break
				}
			}
			ptr[j] = v
			i++

			if *budget < 0 {
				return
			}
		}
	}
}

func mainQSort3(ptr []uint32, block []byte, quadrant []uint16, nblock uint32, loSt, hiSt, dSt int, budget *int) {
	var stackLo, stackHi, stackD [mainQSortStackSize]int
	sp := 0

	stackLo[sp] = loSt
	stackHi[sp] = hiSt
	stackD[sp] = dSt
	sp++

	for sp > 0 {
		sp--
		lo := stackLo[sp]
		hi := stackHi[sp]
		d := stackD[sp]

		if hi-lo < mainQSortSmallThresh || d > mainQSortDepthThresh {
			mainSimpleSort(ptr, block, quadrant, nblock, lo, hi, d, budget)
			if *budget < 0 {
				return
			}
			continue
		}

		med := int(mmed3(block[ptr[lo]+uint32(d)], block[ptr[hi]+uint32(d)], block[ptr[(lo+hi)>>1]+uint32(d)]))

		unLo, ltLo := lo, lo
		unHi, gtHi := hi, hi

		for {
			for {
				if unLo > unHi {
					break
				}
				n := int(block[ptr[unLo]+uint32(d)]) - med
				if n == 0 {
					ptr[unLo], ptr[ltLo] = ptr[ltLo], ptr[unLo]
					ltLo++
					unLo++
					continue
				}
				if n > 0 {
					break
				}
				unLo++
			}
			for {
				if unLo > unHi {
					break
				}
				n := int(block[ptr[unHi]+uint32(d)]) - med
				if n == 0 {
					ptr[unHi], ptr[gtHi] = ptr[gtHi], ptr[unHi]
					gtHi--
					unHi--
					continue
				}
				if n < 0 {
					break
				}
				unHi--
			}
			if unLo > unHi {
				break
			}
			ptr[unLo], ptr[unHi] = ptr[unHi], ptr[unLo]
			unLo++
			unHi--
		}

		if gtHi < ltLo {
			stackLo[sp] = lo
			stackHi[sp] = hi
			stackD[sp] = d + 1
			sp++
			continue
		}

		n := ltLo - lo
		if m := unLo - ltLo; m < n {
			n = m
		}
		vecSwapU32(ptr, lo, unLo-n, n)

		m := hi - gtHi
		if m2 := gtHi - unHi; m2 < m {
			m = m2
		}
		vecSwapU32(ptr, unLo, hi-m+1, m)

		n = lo + unLo - ltLo - 1
		m = hi - (gtHi - unHi) + 1

		// Sort 3 sub-partitions by decreasing size
		nextLo := [3]int{lo, m, n + 1}
		nextHi := [3]int{n, hi, m - 1}
		nextD := [3]int{d, d, d + 1}

		if nextHi[0]-nextLo[0] < nextHi[1]-nextLo[1] {
			nextLo[0], nextLo[1] = nextLo[1], nextLo[0]
			nextHi[0], nextHi[1] = nextHi[1], nextHi[0]
			nextD[0], nextD[1] = nextD[1], nextD[0]
		}
		if nextHi[1]-nextLo[1] < nextHi[2]-nextLo[2] {
			nextLo[1], nextLo[2] = nextLo[2], nextLo[1]
			nextHi[1], nextHi[2] = nextHi[2], nextHi[1]
			nextD[1], nextD[2] = nextD[2], nextD[1]
		}
		if nextHi[0]-nextLo[0] < nextHi[1]-nextLo[1] {
			nextLo[0], nextLo[1] = nextLo[1], nextLo[0]
			nextHi[0], nextHi[1] = nextHi[1], nextHi[0]
			nextD[0], nextD[1] = nextD[1], nextD[0]
		}

		stackLo[sp] = nextLo[0]
		stackHi[sp] = nextHi[0]
		stackD[sp] = nextD[0]
		sp++
		stackLo[sp] = nextLo[1]
		stackHi[sp] = nextHi[1]
		stackD[sp] = nextD[1]
		sp++
		stackLo[sp] = nextLo[2]
		stackHi[sp] = nextHi[2]
		stackD[sp] = nextD[2]
		sp++
	}
}

func mainSortFull(ptr []uint32, block []byte, ftab []uint32, nblock int, budget *int) {
	// Quadrant array placed after block data (in C it shares arr2; here separate)
	quadrant := make([]uint16, nblock+sortOvershoot)

	// Set up 2-byte frequency table
	for i := 0; i <= 65536; i++ {
		ftab[i] = 0
	}

	j := uint16(block[0]) << 8
	i := nblock - 1
	for ; i >= 3; i -= 4 {
		quadrant[i] = 0
		j = (j >> 8) | (uint16(block[i]) << 8)
		ftab[j]++
		quadrant[i-1] = 0
		j = (j >> 8) | (uint16(block[i-1]) << 8)
		ftab[j]++
		quadrant[i-2] = 0
		j = (j >> 8) | (uint16(block[i-2]) << 8)
		ftab[j]++
		quadrant[i-3] = 0
		j = (j >> 8) | (uint16(block[i-3]) << 8)
		ftab[j]++
	}
	for ; i >= 0; i-- {
		quadrant[i] = 0
		j = (j >> 8) | (uint16(block[i]) << 8)
		ftab[j]++
	}

	// Copy overshoot area
	for i := 0; i < sortOvershoot; i++ {
		block[nblock+i] = block[i]
		quadrant[nblock+i] = 0
	}

	// Complete radix sort
	for i := 1; i <= 65536; i++ {
		ftab[i] += ftab[i-1]
	}

	s := uint16(block[0]) << 8
	i = nblock - 1
	for ; i >= 3; i -= 4 {
		s = (s >> 8) | (uint16(block[i]) << 8)
		k := ftab[s] - 1
		ftab[s] = k
		ptr[k] = uint32(i)
		s = (s >> 8) | (uint16(block[i-1]) << 8)
		k = ftab[s] - 1
		ftab[s] = k
		ptr[k] = uint32(i - 1)
		s = (s >> 8) | (uint16(block[i-2]) << 8)
		k = ftab[s] - 1
		ftab[s] = k
		ptr[k] = uint32(i - 2)
		s = (s >> 8) | (uint16(block[i-3]) << 8)
		k = ftab[s] - 1
		ftab[s] = k
		ptr[k] = uint32(i - 3)
	}
	for ; i >= 0; i-- {
		s = (s >> 8) | (uint16(block[i]) << 8)
		k := ftab[s] - 1
		ftab[s] = k
		ptr[k] = uint32(i)
	}

	// Calculate running order from smallest to largest big bucket
	var bigDone [256]bool
	var runningOrder [256]int
	for i := 0; i <= 255; i++ {
		runningOrder[i] = i
	}

	bigFreq := func(b int) uint32 {
		return ftab[(b+1)<<8] - ftab[b<<8]
	}

	h := 1
	for h <= 256 {
		h = 3*h + 1
	}
	for h > 1 {
		h /= 3
		for i := h; i <= 255; i++ {
			vv := runningOrder[i]
			ii := i
			for bigFreq(runningOrder[ii-h]) > bigFreq(vv) {
				runningOrder[ii] = runningOrder[ii-h]
				ii -= h
				if ii <= h-1 {
					break
				}
			}
			runningOrder[ii] = vv
		}
	}

	// Main sorting loop
	numQSorted := 0
	for i := 0; i <= 255; i++ {
		ss := runningOrder[i]

		// Step 1: quicksort unsorted small buckets [ss, j] for j != ss
		for jj := 0; jj <= 255; jj++ {
			if jj != ss {
				sb := (ss << 8) + jj
				if ftab[sb]&uint32(setMask) == 0 {
					lo := int(ftab[sb] & clearMask)
					hi := int(ftab[sb+1]&clearMask) - 1
					if hi > lo {
						mainQSort3(ptr, block, quadrant, uint32(nblock), lo, hi, sortRadix, budget)
						numQSorted += (hi - lo + 1)
						if *budget < 0 {
							return
						}
					}
				}
				ftab[sb] |= uint32(setMask)
			}
		}

		// Step 2: scan to synthesize sorted order for small buckets [t, ss]
		{
			var copyStart, copyEnd [256]int
			for jj := 0; jj <= 255; jj++ {
				copyStart[jj] = int(ftab[(jj<<8)+ss] & clearMask)
				copyEnd[jj] = int(ftab[(jj<<8)+ss+1]&clearMask) - 1
			}
			for jj := int(ftab[ss<<8] & clearMask); jj < copyStart[ss]; jj++ {
				k := int(ptr[jj]) - 1
				if k < 0 {
					k += nblock
				}
				c1 := block[k]
				if !bigDone[c1] {
					ptr[copyStart[c1]] = uint32(k)
					copyStart[c1]++
				}
			}
			for jj := int(ftab[(ss+1)<<8]&clearMask) - 1; jj > copyEnd[ss]; jj-- {
				k := int(ptr[jj]) - 1
				if k < 0 {
					k += nblock
				}
				c1 := block[k]
				if !bigDone[c1] {
					ptr[copyEnd[c1]] = uint32(k)
					copyEnd[c1]--
				}
			}
		}

		for jj := 0; jj <= 255; jj++ {
			ftab[(jj<<8)+ss] |= uint32(setMask)
		}

		// Step 3: update quadrant descriptors
		bigDone[ss] = true

		if i < 255 {
			bbStart := int(ftab[ss<<8] & clearMask)
			bbSize := int(ftab[(ss+1)<<8]&clearMask) - bbStart
			shifts := 0
			for (bbSize >> uint(shifts)) > 65534 {
				shifts++
			}
			for jj := bbSize - 1; jj >= 0; jj-- {
				a2update := ptr[bbStart+jj]
				qVal := uint16(jj >> uint(shifts))
				quadrant[a2update] = qVal
				if int(a2update) < sortOvershoot {
					quadrant[int(a2update)+nblock] = qVal
				}
			}
		}
	}
}
