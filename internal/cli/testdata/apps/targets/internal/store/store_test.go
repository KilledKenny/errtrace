package store

import "testing"

// seed writes the record the tests read back.
func /*@seed*/seed(s *Store) error {
	return s.be.Write("k", []byte("v"))
}

func TestGet(t *testing.T) {
	s := New(NewMem(map[string][]byte{}))
	if err := seed(s); err != nil {
		t.Fatal(err)
	}
	if _, err := s./*@test_call*/Get("k"); err != nil {
		t.Fatal(err)
	}
}
