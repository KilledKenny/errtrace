package codec

// Größe returns the size of raw in bytes ("Größe" is German for size).
func /*@groesse*/Größe(raw []byte) (int, error) {
	if len(raw) == 0 {
		return 0, ErrEmpty
	}
	return len(raw), nil
}

// Länge is Größe for callers that only want the error.
func Länge(raw []byte) error {
		/* «naïve» ✓ → */	_, err := /*@uni_call*/Größe(raw)
	return err
}
