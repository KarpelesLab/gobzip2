package gobzip2

// bzip2 uses a non-standard CRC32 with the AUTODIN-II polynomial (0x04C11DB7)
// and MSB-first (unreflected) bit ordering. This differs from the standard
// CRC32 in hash/crc32 which uses reflected bit ordering.

var crc32Table [256]uint32

func init() {
	const poly = 0x04C11DB7
	for i := range crc32Table {
		crc := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ poly
			} else {
				crc <<= 1
			}
		}
		crc32Table[i] = crc
	}
}

func crcInit() uint32 {
	return 0xFFFFFFFF
}

func crcUpdate(crc uint32, b byte) uint32 {
	return (crc << 8) ^ crc32Table[(crc>>24)^uint32(b)]
}

func crcFinal(crc uint32) uint32 {
	return ^crc
}
