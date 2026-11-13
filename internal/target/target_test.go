package target_test

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/target"
)

func load(t *testing.T) *fixture.Loaded {
	t.Helper()
	return fixture.LoadProgram(t, filepath.Join("testdata", "targets.txtar"))
}

func describe(l *fixture.Loaded, tgt *target.Target) (funcs, sites string) {
	var fs, ss []string
	for _, fn := range tgt.Funcs {
		fs = append(fs, fn.String())
		if tgt.SiteByFunc != nil {
			site := tgt.SiteByFunc[fn]
			if site == nil {
				ss = append(ss, fn.String()+": <nil>")
				continue
			}
			sp, _ := l.SpanOfInstr(site)
			ss = append(ss, fn.String()+": "+strings.TrimPrefix(sp.String(), l.Dir+string(filepath.Separator)))
		}
	}
	return strings.Join(fs, " "), strings.Join(ss, "; ")
}

func TestResolve(t *testing.T) {
	t.Parallel()
	l := load(t)
	tests := []struct {
		ref   string
		kind  target.Kind
		name  string
		span  string // relative to the module directory
		funcs string
		sites string // call sites only
	}{
		{"a.go:@decl_name", target.Declaration, "example.com/fx.Load", "a.go:9:32-36",
			"example.com/fx.Load", ""},
		{"a.go:@decl_kw", target.Declaration, "example.com/fx.Load", "a.go:9:32-36",
			"example.com/fx.Load", ""},
		{"a.go:@ptr_method", target.Declaration, "(*example.com/fx.T).M", "a.go:28:28-29",
			"(*example.com/fx.T).M", ""},
		{"a.go:@val_method", target.Declaration, "(example.com/fx.T).V", "a.go:30:27-28",
			"(example.com/fx.T).V", ""},
		{"a.go:@closure_kw", target.Declaration, "example.com/fx.Closure$1", "a.go:42:24-28",
			"example.com/fx.Closure$1", ""},
		{"a.go:@generic_decl", target.Declaration, "example.com/fx.Map", "a.go:50:23-26",
			"example.com/fx.Map[int] example.com/fx.Map[string]", ""},
		{"a.go:@callee", target.CallSite, "call to helper in example.com/fx.Load", "a.go:15:20-39",
			"example.com/fx.Load", "example.com/fx.Load: a.go:15:20-39"},
		{"a.go:@lparen", target.CallSite, "call to helper in example.com/fx.Load", "a.go:15:20-39",
			"example.com/fx.Load", "example.com/fx.Load: a.go:15:20-39"},
		{"a.go:@sel", target.CallSite, "call to t.M in example.com/fx.Use", "a.go:37:6-19",
			"example.com/fx.Use", "example.com/fx.Use: a.go:37:6-19"},
		{"a.go:@defer", target.CallSite, "call to helper in example.com/fx.Stmts", "a.go:46:31-39",
			"example.com/fx.Stmts", "example.com/fx.Stmts: a.go:46:31-39"},
		{"a.go:@go", target.CallSite, "call to helper in example.com/fx.Stmts", "a.go:47:12-20",
			"example.com/fx.Stmts", "example.com/fx.Stmts: a.go:47:12-20"},
		{"a.go:@generic_call", target.CallSite, "call to Map in example.com/fx.Callers", "a.go:63:29-86",
			"example.com/fx.Callers", "example.com/fx.Callers: a.go:63:29-86"},
		{"a.go:@generic_call_explicit", target.CallSite, "call to Map[string] in example.com/fx.Callers", "a.go:67:37-113",
			"example.com/fx.Callers", "example.com/fx.Callers: a.go:67:37-113"},
		{"a.go:@in_generic", target.CallSite, "call to f in example.com/fx.Map", "a.go:53:28-32",
			"example.com/fx.Map[int] example.com/fx.Map[string]",
			"example.com/fx.Map[int]: a.go:53:28-32; example.com/fx.Map[string]: a.go:53:28-32"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			tgt, err := target.Resolve(l.Program, l.Pos(t, tt.ref))
			if err != nil {
				t.Fatal(err)
			}
			if tgt.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", tgt.Kind, tt.kind)
			}
			if tgt.Name != tt.name {
				t.Errorf("Name = %q, want %q", tgt.Name, tt.name)
			}
			if got := strings.TrimPrefix(tgt.Span.String(), l.Dir+string(filepath.Separator)); got != tt.span {
				t.Errorf("Span = %s, want %s", got, tt.span)
			}
			funcs, sites := describe(l, tgt)
			if funcs != tt.funcs {
				t.Errorf("Funcs = [%s], want [%s]", funcs, tt.funcs)
			}
			if sites != tt.sites {
				t.Errorf("SiteByFunc = {%s}, want {%s}", sites, tt.sites)
			}
			if (tgt.Site != nil) != (tt.kind == target.CallSite) {
				t.Errorf("Site = %v for a %v", tgt.Site, tgt.Kind)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	t.Parallel()
	l := load(t)
	tests := []struct{ ref, want string }{
		{"a.go:@ws", "a.go:7:8: no function declaration or call at position"},
		{"a.go:@variable", "a.go:10:19: no function declaration or call at position"},
		{"a.go:@iface_method", "a.go:33:19: interface method has no body; query a call site"},
		{"a.go:@len", "a.go:12:14: not a call to a function"},
		{"a.go:@conv", "a.go:14:15: not a call to a function"},
		{"a.go:@noerr", "a.go:13:16: count has no error result"},
		{"ignored.go:@ignored", "ignored.go: file is not in any loaded package"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			_, err := target.Resolve(l.Program, l.Pos(t, tt.ref))
			if err == nil {
				t.Fatal("Resolve succeeded, want an error")
			}
			if got := strings.TrimPrefix(err.Error(), l.Dir+string(filepath.Separator)); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKeepReachable(t *testing.T) {
	t.Parallel()
	l := load(t)
	tgt, err := target.Resolve(l.Program, l.Pos(t, "a.go:@generic_decl"))
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Generic == nil || tgt.Generic.String() != "example.com/fx.Map" {
		t.Fatalf("Generic = %v, want example.com/fx.Map", tgt.Generic)
	}
	all, _ := describe(l, tgt)
	tgt.KeepReachable(func(fn *ssa.Function) bool { return false })
	if got, _ := describe(l, tgt); got != all {
		t.Errorf("no reachable instance: Funcs = %s, want them kept: %s", got, all)
	}
	tgt.KeepReachable(func(fn *ssa.Function) bool { return fn.String() == "example.com/fx.Map[string]" })
	if got, _ := describe(l, tgt); got != "example.com/fx.Map[string]" {
		t.Errorf("Funcs = %s, want example.com/fx.Map[string]", got)
	}

	decl, err := target.Resolve(l.Program, l.Pos(t, "a.go:@decl_name"))
	if err != nil {
		t.Fatal(err)
	}
	decl.KeepReachable(func(*ssa.Function) bool { return false })
	if decl.Generic != nil || len(decl.Funcs) != 1 {
		t.Errorf("non-generic Target: Generic %v, Funcs %v", decl.Generic, decl.Funcs)
	}
}
