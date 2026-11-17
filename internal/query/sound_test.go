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
	// wantMaybe: the Verdict must be Maybe: the case pins a guard that
	// must not apply although nothing shows at run time.
	wantMaybe bool
	// notFollowed: the Subject reaches the function's result only by a
	// store through reflection whose destination the upward pass cannot
	// tell. That is its documented limit: if the Verdict is No, it
	// names the stores it did not follow (the "not followed" section).
	// A Verdict that is not No is as good, or better.
	notFollowed bool
	// method is the type of fn's pointer receiver, if fn is a method.
	method string
}

func runSound(t *testing.T, file string, cases []soundCase) {
	t.Helper()
	p := loadProgram(t, file)
	for _, c := range cases {
		fn := c.fn
		if c.method != "" {
			fn = c.method + "." + c.fn
		}
		m := p.membership(t, fn, p.subject(t, c.subject))
		if c.wantNo && len(m.ReflectStores) > 0 {
			t.Errorf("%s, %s: %d stores through reflection were not followed, want none", c.fn, c.subject, len(m.ReflectStores))
		}
		switch {
		case c.notFollowed:
			if !c.truth {
				t.Errorf("%s, %s: a notFollowed case is one the function can return", c.fn, c.subject)
			}
			if m.Verdict == query.No && len(m.ReflectStores) == 0 {
				t.Errorf("%s, %s: Verdict is no and names no store through reflection that was not followed, but the function can return it", c.fn, c.subject)
			}
		case c.truth && m.Verdict == query.No:
			t.Errorf("%s, %s: Verdict is no, but the function can return it", c.fn, c.subject)
		case !c.truth && m.Verdict == query.Yes:
			t.Errorf("%s, %s: Verdict is yes, but the function cannot return it", c.fn, c.subject)
		case c.wantMaybe && m.Verdict != query.Maybe:
			t.Errorf("%s, %s: Verdict is %v, want maybe", c.fn, c.subject, m.Verdict)
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
		{fn: "C", subject: "is ErrZ", wantNo: true},
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
		{fn: "CustomAs", subject: "as *E", truth: true},
		{fn: "Deep", subject: "is ErrX", truth: true},
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
		// A wrapper may come to wrap the Sentinel after errors.Is said
		// no; the Sentinel itself may not.
		{fn: "Mutated", subject: "is ErrX", truth: true},
		{fn: "ViaCallee", subject: "is ErrX", truth: true},
		{fn: "Stateful", subject: "is ErrX", truth: true},
		{fn: "MutatedPtr", subject: "is ErrX", truth: true},
		{fn: "CheckedValue", subject: "is ErrX", wantNo: true},
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
		{fn: "Lookup", subject: "is ErrX", truth: true},
		{fn: "ReadAll", subject: "is ErrX", truth: true},
		{fn: "RunHook", subject: "is ErrX", truth: true, method: "Client"},
		{fn: "RunOnErr", subject: "is ErrX", truth: true, method: "Client"},
		{fn: "Last", subject: "is ErrX", truth: true, method: "Client"},
		{fn: "First", subject: "is ErrX", truth: true},
		{fn: "FromMap", subject: "is ErrX", truth: true},
		{fn: "FromChan", subject: "is ErrX", truth: true},
		{fn: "FromReq", subject: "is ErrX", truth: true},
		{fn: "FromPtr", subject: "is ErrX", truth: true},
		{fn: "FromAny", subject: "is ErrX", truth: true},
		{fn: "Each", subject: "is ErrX", truth: true},
		{fn: "GetDefault", subject: "is ErrX", truth: true},
		{fn: "New", subject: "is ErrX", truth: true},
		{fn: "Walk", subject: "is ErrX", truth: true},
		// A Sentinel by its name is not taken to be reassigned.
		{fn: "Other", subject: "is ErrX", wantNo: true},
	})
}

// TestVerdictSoundWild does the same for a program with Is methods that do
// not look at their target and with calls and stores through reflection.
func TestVerdictSoundWild(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_wild.txtar", []soundCase{
		{fn: "OpaqueIs", subject: "is ErrX", truth: true},
		{fn: "Reflect", subject: "is ErrX", truth: true},
		{fn: "IsAny", subject: "is ErrX", truth: true},
		{fn: "IsNonNil", subject: "is ErrX", truth: true},
		{fn: "IsAny", subject: "is ErrZ", truth: true},
		{fn: "ReflectCall", subject: "is ErrX", truth: true},
		{fn: "ReflectAppend", subject: "is ErrX", truth: true},
		{fn: "ReflectMethod", subject: "is ErrX", truth: true},
		{fn: "ReflectSet", subject: "is ErrX", truth: true},
		{fn: "ReflectSend", subject: "is ErrX", truth: true},
		{fn: "TemplateMethod", subject: "is ErrX", truth: true},
		{fn: "TemplateFuncMap", subject: "is ErrX", truth: true},
		{fn: "TemplatePanics", subject: "is ErrX", truth: true},
	})
}

// TestVerdictSoundReflect does the same for stores through reflection:
// one whose destination the upward pass can tell is followed, and one
// whose destination it cannot tell is reported as not followed.
func TestVerdictSoundReflect(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_reflect.txtar", []soundCase{
		{fn: "FreshWhole", subject: "is ErrA", truth: true},
		{fn: "FreshHelper", subject: "is ErrB", truth: true},
		{fn: "FreshStruct", subject: "is ErrC", truth: true},
		{fn: "FreshField", subject: "is ErrD", truth: true},
		{fn: "FreshAddr", subject: "is ErrE", truth: true},
		{fn: "FreshSlice", subject: "is ErrF", truth: true},
		{fn: "FreshMap", subject: "is ErrG", truth: true},
		{fn: "FreshChan", subject: "is ErrH", truth: true},
		{fn: "FreshParam", subject: "is ErrI", truth: true},
		{fn: "KnownParam", subject: "is ErrJ", truth: true},
		{fn: "KnownHelper", subject: "is ErrK", truth: true},
		{fn: "FreshMapIter", subject: "is ErrL", truth: true},
		{fn: "KnownMapIter", subject: "is ErrM", truth: true},
		{fn: "KnownSeq", subject: "is ErrP", truth: true},
		{fn: "FreshSeq", subject: "is ErrQ", truth: true},
		{fn: "LazyHeld", subject: "is ErrR", truth: true},
		{fn: "DeepRead", subject: "is ErrS", truth: true},
		{fn: "HeldReceiver", subject: "is ErrU", truth: true},
		{fn: "FreshAwayReflect", subject: "is ErrV", truth: true},
		// Fresh memory of a struct type that is an error itself.
		{fn: "FreshErrStruct", subject: "is ErrX1", truth: true},
		{fn: "FreshErrIface", subject: "is ErrX2", truth: true},
		// reflect.TypeAssert, and the pointer Value.Addr makes.
		{fn: "TypeAssertGeneric", subject: "is ErrY1", truth: true},
		{fn: "TypeAssertStruct", subject: "is ErrY2", truth: true},
		{fn: "TypeAssertAddr", subject: "is ErrY3", truth: true},
		{fn: "BoxConcrete", subject: "is ErrY4", truth: true},
		// A value made by reflection of a type every value of which is
		// marked.
		{fn: "EqReflectNew", subject: "is ErrE1", truth: true},
		{fn: "EqConvert", subject: "is ErrE2", truth: true},
		{fn: "NewConcrete", subject: "as *CT8", truth: true},
		{fn: "EqReflectNew", subject: "is ErrE2", wantNo: true},
		{fn: "TypeAssertGeneric", subject: "is ErrX1", wantNo: true},
		// The documented limit.
		{fn: "FreshAwayTyped", subject: "is ErrW", truth: true, notFollowed: true},
		{fn: "KnownCross", subject: "is ErrT", truth: true, notFollowed: true},
		{fn: "FreshEscape", subject: "is ErrN", truth: true, notFollowed: true},
		{fn: "UnknownGlobal", subject: "is ErrO", truth: true, notFollowed: true},
		// A store that is followed marks what it writes, not the program.
		{fn: "Clean", subject: "is ErrA", wantNo: true},
		{fn: "Clean", subject: "is ErrC", wantNo: true},
		{fn: "Clean", subject: "is ErrD", wantNo: true},
		{fn: "Clean", subject: "is ErrG", wantNo: true},
		{fn: "FreshWhole", subject: "is ErrB", wantNo: true},
		{fn: "FreshField", subject: "is ErrC", wantNo: true},
	})
}

// TestVerdictSoundValues does the same for what a value holds: a
// Sentinel whose value can change, fields that no Unwrap, Is or As method
// reports, and composites in interface values.
func TestVerdictSoundValues(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_values.txtar", []soundCase{
		// A failed errors.Is excludes only a Sentinel whose value is
		// fixed.
		{fn: "MutatedSentinel", subject: "is ErrMS", truth: true},
		{fn: "GuardWrapperSentinel", subject: "is ErrPW", wantMaybe: true},
		{fn: "GuardOverWrapper", subject: "is ErrOver", wantMaybe: true},
		{fn: "GuardPlain", subject: "is ErrPlain", wantNo: true},
		{fn: "GuardBuilt", subject: "is ErrBuilt", wantNo: true},
		{fn: "GuardCode", subject: "is ErrCode", wantNo: true},
		// What errors.Is and errors.As find in a value is what its
		// Unwrap, Is and As methods report.
		{fn: "ValOnly", subject: "is ErrVal", wantNo: true},
		{fn: "IsField", subject: "is ErrIsF", truth: true},
		{fn: "EqField", subject: "is ErrEq", truth: true},
		{fn: "HelperUnwrap", subject: "is ErrGet", truth: true},
		{fn: "Promoted", subject: "is ErrEmb", truth: true},
		{fn: "MultiUnwrap", subject: "is ErrMulti", truth: true},
		{fn: "AsField", subject: "as *AT", truth: true},
		{fn: "IsField", subject: "is ErrEq", wantNo: true},
		// Composites in interface values.
		{fn: "AnyBox", subject: "is ErrBox", truth: true},
		{fn: "OtherSlice", subject: "is ErrAny", wantNo: true},
	})
}

// TestVerdictSoundMethods does the same for what the methods of a type
// say: a wrapper type is judged by its own methods and not by those of a
// type it converts to, the errors of an Unwrap() []error are found, and
// an Is or As method that can answer true for the Subject makes its type
// a wrapper of it, whichever side of a look at the target the answer is
// on.
func TestVerdictSoundMethods(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_methods.txtar", []soundCase{
		// Wrapper types of one conversion class.
		{fn: "CodeIs", subject: "is ErrC1", truth: true},
		{fn: "OtherIs", subject: "is ErrC1o", truth: true},
		{fn: "CodeIs", subject: "is ErrC1o", wantNo: true},
		{fn: "OtherIs", subject: "is ErrC1", wantNo: true},
		{fn: "CodeFromOther", subject: "is ErrC1", truth: true},
		{fn: "OtherFromCode", subject: "is ErrC1", wantNo: true},
		{fn: "CodeUnwrap", subject: "is ErrC1u", truth: true},
		{fn: "CodeUnwrap", subject: "is ErrC1", wantNo: true},
		{fn: "CodeIs", subject: "is ErrC1u", wantNo: true},
		{fn: "ArrCode", subject: "is ErrC1a", truth: true},
		{fn: "ArrOther", subject: "is ErrC1a", wantNo: true},
		// Unwrap() []error.
		{fn: "JoinSlice", subject: "is ErrC2j", truth: true},
		{fn: "ErrorfSlice", subject: "is ErrC2f", truth: true},
		{fn: "WalkSlice", subject: "is ErrC2w", truth: true},
		{fn: "EscapedSlice", subject: "is ErrC2e", truth: true},
		{fn: "HandMulti", subject: "is ErrC2m", truth: true},
		{fn: "JoinSlice", subject: "is ErrC2f", wantNo: true},
		{fn: "ErrorfSlice", subject: "is ErrC2j", wantNo: true},
		// Is and As methods: true on the far side of the look.
		{fn: "IsNegBranch", subject: "is ErrN1", truth: true},
		{fn: "IsNegBranch2", subject: "is ErrN2", truth: true},
		{fn: "IsSwitchDefault", subject: "is ErrN3", truth: true},
		{fn: "IsFlag", subject: "is ErrN4", truth: true},
		{fn: "IsAnd", subject: "is ErrN5", truth: true},
		{fn: "IsHelperNeg", subject: "is ErrN6", truth: true},
		{fn: "IsTypeSwitch", subject: "is ErrN7", truth: true},
		{fn: "IsIfaceOK", subject: "is ErrN8", truth: true},
		{fn: "IsIfaceMethod", subject: "is ErrN9", truth: true},
		{fn: "AsNeg", subject: "as *T10", truth: true},
		// Is and As methods that say what they look for.
		{fn: "IsEq", subject: "is ErrP1", truth: true},
		{fn: "IsEq", subject: "is ErrP2", wantNo: true},
		{fn: "IsEqAnd", subject: "is ErrP3", truth: true},
		{fn: "IsEqAnd", subject: "is ErrP2", wantNo: true},
		{fn: "IsHelper", subject: "is ErrP4", truth: true},
		{fn: "IsHelper", subject: "is ErrP2", wantNo: true},
		{fn: "IsOwnType", subject: "is ErrP5", truth: true},
		{fn: "IsOwnType", subject: "is ErrP2", wantNo: true},
		{fn: "IsSwitch", subject: "is ErrP6", truth: true},
		{fn: "IsSwitch", subject: "is ErrP2", wantNo: true},
		{fn: "IsNeqGuard", subject: "is ErrP7", truth: true},
		{fn: "IsNeqGuard", subject: "is ErrP2", wantNo: true},
		{fn: "IsNotOK", subject: "is ErrP2", wantNo: true},
		{fn: "AsEq", subject: "as *T10", truth: true},
		{fn: "AsEq", subject: "as *U10", wantNo: true},
	})
}
