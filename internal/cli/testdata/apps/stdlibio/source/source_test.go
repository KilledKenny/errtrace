package source

import (
	"errors"
	"testing"
)

var errFixture = errors.New("fixture")

func /*@failing*/failing() error { return errFixture }

func TestMagicEmpty(t *testing.T) {
	if err := failing(); err == nil {
		t.Fatal("want error")
	}
	if _, err := Header("", 1); err != nil {
		t.Fatal(err)
	}
}
