package cli

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestClosedInternal checks which internal packages are not open: only
// those of the main module that the default patterns load whole.
func TestClosedInternal(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	mainMod := &packages.Module{Path: "example.com/m", Main: true, Dir: root}
	depMod := &packages.Module{Path: "example.com/dep", Dir: other}
	tests := []struct {
		name     string
		pkg      *packages.Package
		dir      string
		patterns []string
		want     bool
	}{
		{"whole module", &packages.Package{PkgPath: "example.com/m/internal/x", Module: mainMod}, root, []string{"./..."}, true},
		{"nested internal", &packages.Package{PkgPath: "example.com/m/a/internal/x/y", Module: mainMod}, root, []string{"./..."}, true},
		{"not internal", &packages.Package{PkgPath: "example.com/m/x", Module: mainMod}, root, []string{"./..."}, false},
		{"other patterns", &packages.Package{PkgPath: "example.com/m/internal/x", Module: mainMod}, root, []string{"./internal/..."}, false},
		{"two patterns", &packages.Package{PkgPath: "example.com/m/internal/x", Module: mainMod}, root, []string{"./...", "example.com/dep/..."}, false},
		{"below the module root", &packages.Package{PkgPath: "example.com/m/internal/x", Module: mainMod}, filepath.Join(root, "internal"), []string{"./..."}, false},
		{"another module", &packages.Package{PkgPath: "example.com/dep/internal/x", Module: depMod}, root, []string{"./..."}, false},
		{"standard library", &packages.Package{PkgPath: "internal/poll"}, root, []string{"./..."}, false},
	}
	for _, tt := range tests {
		if got := closedInternal(tt.pkg, wholeModuleDir(tt.dir, tt.patterns)); got != tt.want {
			t.Errorf("%s: closedInternal = %v, want %v", tt.name, got, tt.want)
		}
	}
}
