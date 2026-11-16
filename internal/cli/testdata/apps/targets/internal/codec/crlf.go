package codec

// Checksum sums the bytes of raw. This file has CRLF line endings.
func /*@checksum*/Checksum(raw []byte) (uint32, error) {
	if _, err := /*@trim_call*/Trim(raw); err != nil {
		return 0, err
	}
	var sum uint32
	for _, c := range raw {
		sum += uint32(c)
	}
	return sum, nil
}
