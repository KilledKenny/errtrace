package store_test

import (
	"testing"

	"example.com/targets/internal/store"
)

// roundTrip writes and reads one record through the exported API.
func /*@roundtrip*/roundTrip(be store.Backend) error {
	if err := be.Write("k", []byte("v")); err != nil {
		return err
	}
	_, err := store.New(be).Get("k")
	return err
}

func TestRoundTrip(t *testing.T) {
	if err := roundTrip(store.NewMem(map[string][]byte{})); err != nil {
		t.Fatal(err)
	}
}
