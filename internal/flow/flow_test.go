package flow

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

const src = `package p

type PtrErr struct{}

func (*PtrErr) Error() string { return "" }

type ValErr struct{}

func (ValErr) Error() string { return "" }

type Temporary interface {
	error
	Temporary() bool
}

type Plain struct{}

func G[E error, T any]() (E, T) { var e E; var t T; return e, t }

var (
	vErr       error
	vPtr       *PtrErr
	vPtrBase   PtrErr
	vVal       ValErr
	vValPtr    *ValErr
	vTemporary Temporary
	vPlain     Plain
	vInt       int
	vAny       any
)
`

func TestIsErrorType(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{
		"vErr":       true,
		"vPtr":       true,
		"vPtrBase":   true, // *PtrErr implements error
		"vVal":       true,
		"vValPtr":    true,
		"vTemporary": true,
		"vPlain":     false,
		"vInt":       false,
		"vAny":       false,
	}
	for name, want := range tests {
		typ := pkg.Scope().Lookup(name).Type()
		if got := IsErrorType(typ); got != want {
			t.Errorf("IsErrorType(%s %s) = %v, want %v", name, typ, got, want)
		}
	}
	sig := pkg.Scope().Lookup("G").Type().(*types.Signature)
	if got := ErrorResults(sig); len(got) != 1 || got[0] != 0 {
		t.Errorf("ErrorResults(G) = %v, want [0]", got)
	}
	if IsErrorType(nil) {
		t.Error("IsErrorType(nil) = true")
	}
}
