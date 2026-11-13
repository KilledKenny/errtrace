package query_test

import (
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/query"
	"github.com/KilledKenny/errtrace/internal/resolve"
	"github.com/KilledKenny/errtrace/internal/target"
)

// program is a loaded fixture with its Resolver, set up as the CLI does.
type program struct {
	l    *fixture.Loaded
	pkg  *ssa.Package
	base *resolve.VTA
	open []*ssa.Package
}

func loadProgram(t *testing.T, file string) *program {
	t.Helper()
	l := fixture.LoadProgram(t, filepath.Join("testdata", file))
	p := &program{l: l, pkg: l.SSAPackage(l.Initial[0])}
	initial := []*ssa.Package{p.pkg}
	v, err := resolve.NewVTA(t.Context(), l.SSA, resolve.Roots(l.SSA, initial))
	if err != nil {
		t.Fatal(err)
	}
	// A package that is not a main package is open, as in the CLI.
	for _, pkg := range initial {
		if pkg.Pkg.Name() != "main" {
			p.open = append(p.open, pkg)
		}
	}
	if len(p.open) > 0 {
		v.SetOpen()
	}
	p.base = v
	return p
}

// subject resolves "is <pkgpath>.<Name>" or "as [*]<pkgpath>.<Name>"; a
// name without a package path is in the fixture's package.
func (p *program) subject(t *testing.T, s string) mark.Subject {
	t.Helper()
	kind, name, _ := strings.Cut(s, " ")
	pointer := strings.HasPrefix(name, "*")
	name = strings.TrimPrefix(name, "*")
	pkg := p.pkg
	if i := strings.LastIndex(name, "."); i >= 0 {
		pkg = p.l.SSA.ImportedPackage(name[:i])
		name = name[i+1:]
	}
	if pkg == nil {
		t.Fatalf("subject %q: no such package", s)
	}
	switch kind {
	case "is":
		g, ok := pkg.Members[name].(*ssa.Global)
		if !ok {
			t.Fatalf("subject %q: no such variable", s)
		}
		return mark.Subject{Sentinel: g}
	case "as":
		obj, ok := pkg.Pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			t.Fatalf("subject %q: no such type", s)
		}
		typ := types.Unalias(obj.Type())
		if pointer {
			typ = types.NewPointer(typ)
		}
		return mark.Subject{Type: typ}
	}
	t.Fatalf("bad subject %q", s)
	return mark.Subject{}
}

// membership runs the Membership Query of subj on the declaration of the
// function fn of the fixture's package.
func (p *program) membership(t *testing.T, fn string, subj mark.Subject) *query.MembershipResult {
	t.Helper()
	f := p.pkg.Func(fn)
	if typ, name, ok := strings.Cut(fn, "."); ok {
		// A method of a pointer receiver: Type.Method.
		f = nil
		if tn := p.pkg.Type(typ); tn != nil {
			f = p.l.SSA.LookupMethod(types.NewPointer(tn.Type()), p.pkg.Pkg, name)
		}
	}
	if f == nil {
		t.Fatalf("no function %s", fn)
	}
	tgt := &target.Target{Kind: target.Declaration, Name: f.String(), Funcs: []*ssa.Function{f}}
	v := p.base
	open := p.open
	if !v.Reachable(f) {
		// The package of a Target that nothing reaches is open too.
		var err error
		if v, err = p.base.WithRoots(t.Context(), tgt.Funcs); err != nil {
			t.Fatal(err)
		}
		if len(open) == 0 {
			open = []*ssa.Package{p.pkg}
		}
	}
	m, err := query.Membership(t.Context(), p.l.Program, tgt, query.MembershipOptions{
		Options: query.Options{Resolver: v},
		Subject: subj,
		Upper:   v.Upper(),
		Open:    open,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMembership(t *testing.T) {
	t.Parallel()
	p := loadProgram(t, "membership.txtar")
	tests := []struct {
		target, sentinel string
		verdict          query.Verdict
		matches          int
		// carriers are the details of the Carriers' leaves, in order.
		carriers string
		reached  bool
	}{
		{"Yes", "ErrX", query.Yes, 1, "", false},
		{"Yes", "ErrY", query.No, 0, "", false},
		{"Handled", "ErrU", query.Maybe, 1, "", false},
		// The key case: an Unresolved entry that cannot carry ErrX.
		{"Hole", "ErrX", query.No, 0, "", false},
		// other matches the call and returns ErrY.
		{"Hole", "ErrY", query.Maybe, 0, "dynamic call to function value example.com/fx.hook", true},
		{"Stored", "ErrX", query.Maybe, 0, "load of field err of *example.com/fx.box", true},
		{"Stored", "ErrY", query.No, 0, "", false},
		// Of three Unresolved entries only the field that holds ErrX is a
		// Carrier.
		{"Two", "ErrX", query.Maybe, 0, "load of field err of *example.com/fx.box", true},
		{"Two", "ErrY", query.Maybe, 0, "dynamic call to function value example.com/fx.hook", true},
		// errors.Is(err, ErrX) guards the return: the upward pass stops
		// the Mark there and does not reach the Target.
		{"Guarded", "ErrX", query.No, 0, "", false},
		{"Guarded", "ErrY", query.Yes, 1, "", false},
		{"Lib", "ErrX", query.No, 0, "", false},
	}
	for _, tt := range tests {
		m := p.membership(t, tt.target, p.subject(t, "is "+tt.sentinel))
		var carriers []string
		for _, c := range m.Carriers {
			leaf := m.Table.Get(c.Entry.Leaf)
			if c.Unresolved != (leaf.Kind == errset.Unresolved) {
				t.Errorf("%s %s: Carrier %v: Unresolved = %v", tt.target, tt.sentinel, leaf.Kind, c.Unresolved)
			}
			if len(c.Trace) == 0 {
				t.Errorf("%s %s: Carrier %q has no trace", tt.target, tt.sentinel, leaf.Detail)
			}
			carriers = append(carriers, leaf.Detail)
		}
		got := strings.Join(carriers, "; ")
		if m.Verdict != tt.verdict || len(m.Matches) != tt.matches || got != tt.carriers || m.Reached != tt.reached {
			t.Errorf("Membership(%s, %s) = %v, %d matches, carriers %q, reached %v; want %v, %d, %q, %v",
				tt.target, tt.sentinel, m.Verdict, len(m.Matches), got, m.Reached,
				tt.verdict, tt.matches, tt.carriers, tt.reached)
		}
	}
}
