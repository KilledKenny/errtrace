package query_test

import (
	"testing"

	"github.com/KilledKenny/errtrace/internal/query"
)

// soundCase is one Membership Query with a known answer: what errors.Is
// (for "is") or errors.As (for "as") reports about the error the function
// returns when the program runs (or, in a library, can report when code
// outside the program calls it).
type soundCase struct {
	fn      string
	subject string
	// truth: the error can be the Subject. The Verdict must then not be
	// No. If it cannot, the Verdict must not be Yes; and it must be No
	// where wantNo is set (the cases the upward pass is precise about).
	truth  bool
	wantNo bool
}

func runSound(t *testing.T, file string, cases []soundCase) {
	t.Helper()
	p := loadProgram(t, file)
	for _, c := range cases {
		m := p.membership(t, c.fn, p.subject(t, c.subject))
		switch {
		case c.truth && m.Verdict == query.No:
			t.Errorf("%s, %s: Verdict is no, but the function can return it", c.fn, c.subject)
		case !c.truth && m.Verdict == query.Yes:
			t.Errorf("%s, %s: Verdict is yes, but the function cannot return it", c.fn, c.subject)
		case c.wantNo && m.Verdict != query.No:
			t.Errorf("%s, %s: Verdict is %v, want no", c.fn, c.subject, m.Verdict)
		}
	}
}

// TestVerdictSound checks the Verdict against the runtime answer on a
// whole program: it is never "no" when errors.Is or errors.As is true.
func TestVerdictSound(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_main.txtar", []soundCase{
		// A Concrete Type in the Wrap Chain is found by errors.As.
		{fn: "Custom", subject: "as *W", truth: true},
		{fn: "Open", subject: "as *io/fs.PathError", truth: true},
		{fn: "Sys", subject: "as *os.SyscallError", truth: true},
		{fn: "Nested", subject: "as *W", truth: true},
		{fn: "Custom", subject: "as *os.SyscallError", wantNo: true},
		{fn: "Sys", subject: "as *W"},
		// Resolved entries that hold the Sentinel.
		{fn: "A", subject: "is ErrX", truth: true},
		{fn: "B", subject: "is ErrX", truth: true},
		{fn: "C", subject: "is ErrX", truth: true},
		{fn: "D", subject: "is ErrM", truth: true},
		{fn: "OnceSaved", subject: "is ErrX", truth: true},
		{fn: "Late", subject: "is ErrX", truth: true},
		{fn: "FuncWrap", subject: "is ErrX", truth: true},
		{fn: "MapWrap", subject: "is ErrX", truth: true},
		{fn: "ChanWrap", subject: "is ErrX", truth: true},
		{fn: "Embedded", subject: "as Inner", truth: true},
		{fn: "Value", subject: "is ErrV", truth: true},
		{fn: "RetE", subject: "as *E", truth: true},
		{fn: "TypedNil", subject: "as *E", truth: true},
		{fn: "TypedNil", subject: "is ErrP", truth: true},
		{fn: "A", subject: "is ErrZ", wantNo: true},
		// (C is marked for ErrZ too: IsOpaque, whose Is method cannot be
		// seen, wraps every error, and a dead method reaches it by CHA.)
		{fn: "C", subject: "is ErrZ"},
		// Intrinsics called through function values.
		{fn: "DynErrorf", subject: "is ErrX", truth: true},
		{fn: "DynJoin", subject: "is ErrX", truth: true},
		// A promoted method.
		{fn: "Promoted", subject: "is ErrI", truth: true},
		// Custom Is and As methods.
		{fn: "CustomIs", subject: "is ErrX", truth: true},
		{fn: "CustomIs", subject: "is ErrZ", wantNo: true},
		{fn: "Open", subject: "is io/fs.ErrNotExist", truth: true},
		{fn: "DelegIs", subject: "is ErrX", truth: true},
		{fn: "OpaqueIs", subject: "is ErrX", truth: true},
		{fn: "CustomAs", subject: "as *E", truth: true},
		{fn: "Deep", subject: "is ErrX", truth: true},
		{fn: "Reflect", subject: "is ErrX", truth: true},
		// Guards: only errors.Is excludes the Sentinel.
		{fn: "GuardIs", subject: "is ErrX", wantNo: true},
		{fn: "GuardNeq", subject: "is ErrX", truth: true},
		{fn: "GuardNot", subject: "is ErrX", truth: true},
		{fn: "GuardReassign", subject: "is ErrX", truth: true},
		{fn: "GuardOther", subject: "is ErrX", truth: true},
		// Panics.
		{fn: "Recovered", subject: "is ErrX", truth: true},
		{fn: "RecoveredOther", subject: "is ErrX", wantNo: true},
		{fn: "Clean", subject: "is ErrX", wantNo: true},
		{fn: "Clean", subject: "as *W", wantNo: true},
	})
}

// TestVerdictSoundLibrary does the same for a program without a main
// package, where the callers are not all loaded.
func TestVerdictSoundLibrary(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_lib.txtar", []soundCase{
		{fn: "Hook", subject: "is ErrX", truth: true},
		{fn: "Param", subject: "is ErrX", truth: true},
		{fn: "Param", subject: "is io.EOF", truth: true},
		{fn: "GetSaved", subject: "is ErrX", truth: true},
		{fn: "Callback", subject: "is io.EOF", truth: true},
		{fn: "GetErr", subject: "is ErrX", truth: true},
		{fn: "GetField", subject: "is ErrX", truth: true},
		{fn: "unexported", subject: "is ErrX", truth: true},
		{fn: "Clean", subject: "is ErrX", wantNo: true},
		{fn: "Clean", subject: "is io.EOF", wantNo: true},
	})
}
