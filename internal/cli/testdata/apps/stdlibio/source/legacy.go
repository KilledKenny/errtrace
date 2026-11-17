//go:build legacy

package source

// Legacy is the pre-v2 reader. It no longer compiles (decodeLegacy was
// removed) and is kept out of every normal build by its tag.
func /*@legacy*/Legacy(path string) error {
	return decodeLegacy(path)
}
