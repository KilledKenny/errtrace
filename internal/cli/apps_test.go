package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilledKenny/errtrace/internal/fixture"
)

// TestApps runs every query of every test application under testdata/apps.
// An application is a Go module kept as ordinary files, with one txtar
// archive per Query in its queries directory (see testdata/apps/README.md).
// Use -update to rewrite the stdout, stderr and exit sections.
func TestApps(t *testing.T) {
	root := filepath.Join("testdata", "apps")
	apps, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if !app.IsDir() {
			continue
		}
		appDir := filepath.Join(root, app.Name())
		queries, err := filepath.Glob(filepath.Join(appDir, fixture.AppQueriesDir, "*.txtar"))
		if err != nil {
			t.Fatal(err)
		}
		if len(queries) == 0 {
			t.Errorf("%s: no queries", appDir)
		}
		for _, q := range queries {
			name := app.Name() + "/" + strings.TrimSuffix(filepath.Base(q), ".txtar")
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fixture.RunApp(t, appDir, q, runCLI)
			})
		}
	}
}
