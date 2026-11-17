package query_test

import (
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/mark"
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
	// wantUnsure: the function can return the Subject, and no entry of
	// its Error Set is the Subject: a Yes would be as wrong as a No. The
	// Verdict must be Maybe, or a No apart from reflection.
	wantUnsure bool
	// wantApart: the Verdict must be a No apart from reflection: the
	// Subject reaches the function only through a store through
	// reflection that the upward pass did not follow, and the flooding
	// second pass shows that such a store can matter.
	wantApart bool
	// notFollowed: the Subject reaches the function's result only by a
	// store through reflection whose destination the upward pass cannot
	// tell. That is its documented limit: if the Verdict is No, it is
	// a No apart from reflection, which names the stores it did not
	// follow (the "not followed" section) and exits as a Maybe. A
	// Verdict that is not No is as good, or better.
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
		case c.wantApart:
			if !m.ApartFromReflection() {
				t.Errorf("%s, %s: Verdict is %v with %d stores through reflection not followed, want no, apart from reflection", c.fn, c.subject, m.Verdict, len(m.ReflectStores))
			}
		case c.wantUnsure:
			if !c.truth {
				t.Errorf("%s, %s: a wantUnsure case is one the function can return", c.fn, c.subject)
			}
			if m.Verdict != query.Maybe && !m.ApartFromReflection() {
				t.Errorf("%s, %s: Verdict is %v, want maybe or no, apart from reflection", c.fn, c.subject, m.Verdict)
			}
		case c.notFollowed:
			if !c.truth {
				t.Errorf("%s, %s: a notFollowed case is one the function can return", c.fn, c.subject)
			}
			if m.Verdict == query.No && !m.ApartFromReflection() {
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

// TestVerdictSoundSlices does the same for a slice parameter that a
// function passes on to another, without reflection: the argument array of
// a variadic call is memory of its own only if no function it reaches
// lets the slice escape.
func TestVerdictSoundSlices(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_slices.txtar", []soundCase{
		{fn: "FwdCopy", subject: "is ErrCopy", truth: true},
		{fn: "FwdAppend", subject: "is ErrAppend", truth: true},
		{fn: "FwdStored", subject: "is ErrStored", truth: true},
		{fn: "FwdReturned", subject: "is ErrReturned", truth: true},
		{fn: "FwdTwice", subject: "is ErrTwice", truth: true},
		{fn: "FwdCycle", subject: "is ErrCycle", truth: true},
		{fn: "FwdJoin", subject: "is ErrJoin", truth: true},
		{fn: "FwdErrorf", subject: "is ErrErrorf", truth: true},
		{fn: "FwdAny", subject: "is ErrAny", truth: true},
		// The slice stays with the functions it is passed on to: what
		// other memory of type error holds is not in it.
		{fn: "FwdReadOnly", subject: "is ErrPool", wantNo: true},
		{fn: "FwdElemStore", subject: "is ErrPool", wantNo: true},
	})
}

// TestVerdictSoundOperands does the same for a method that an intrinsic
// calls on an operand (fmt.Errorf formats it, the error of errors.Join
// asks it), without reflection: the bodies of the intrinsics are not
// scanned, so the receiver of such a method gets the Mark at the call of
// the intrinsic, and a panic of a method that package fmt calls is seen
// by the recovers that cover a call of fmt.Errorf.
func TestVerdictSoundOperands(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_operands.txtar", []soundCase{
		{fn: "ErrorfRecv", subject: "is ErrO1", truth: true},
		{fn: "ErrorfPlain", subject: "is ErrO2", truth: true},
		{fn: "ErrorfFormat", subject: "is ErrO3", truth: true},
		{fn: "ErrorfChan", subject: "is ErrO4", truth: true},
		{fn: "JoinThenError", subject: "is ErrO5", truth: true},
		// The methods an intrinsic can call publish nothing; and an
		// operand is not the receiver of a method of another type.
		{fn: "Quiet", subject: "is ErrO6", wantNo: true},
		{fn: "OtherType", subject: "is ErrO7", wantNo: true},
		// A method that package fmt calls inside fmt.Errorf panics:
		// the panic leaves the call of fmt.Errorf.
		{fn: "PanicError", subject: "is ErrO8", truth: true},
		{fn: "PanicErrorWrapped", subject: "is ErrO9", truth: true},
		{fn: "PanicFormat", subject: "is ErrO10", truth: true},
		{fn: "PanicOfPanicValue", subject: "is ErrO11", truth: true},
		// It panics with a value that is not the Subject.
		{fn: "PanicOther", subject: "is ErrO12", wantNo: true},
		// The verbs of a constant format say which methods are called.
		{fn: "VerbNoError", subject: "is ErrO13", wantNo: true},
		{fn: "VerbFormat", subject: "is ErrO14", truth: true},
		{fn: "VerbGoString", subject: "is ErrO15", truth: true},
		{fn: "VerbExtra", subject: "is ErrO16", truth: true},
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
		// It cannot be ErrE2 (the function makes a code6). The Verdict
		// is a No apart from reflection all the same: a store of a made
		// value is recorded for ErrE2 (in decode), and the function
		// reads through reflection (v.Interface()), which the flooding
		// pass cannot tell from a read of what that store wrote.
		{fn: "EqReflectNew", subject: "is ErrE2", wantApart: true},
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
		// A store of a value that reflection made of a type chosen at
		// run time is recorded like any other: it may put a value of the
		// Subject's type into an interface variable. It cannot matter
		// to a function that reads no memory.
		{fn: "ReadsMemory", subject: "as *CT8", wantApart: true},
		{fn: "ReadsMemory", subject: "is ErrE1", wantApart: true},
		{fn: "Clean", subject: "as *CT8", wantNo: true},
		{fn: "Clean", subject: "is ErrE1", wantNo: true},
		// A store that is not followed cannot matter to a function that
		// reads no memory; it can to one that reads a field.
		{fn: "Clean", subject: "is ErrO", wantNo: true},
		{fn: "ReadsMemory", subject: "is ErrO", wantApart: true},
	})
}

// TestVerdictSoundFlood does the same for functions that the Subject
// reaches only through a store through reflection that the upward pass
// does not follow: the flooding pass must reach each of them (a No apart
// from reflection, never a plain No), and must not reach a function that
// no such store can matter to.
func TestVerdictSoundFlood(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_flood.txtar", []soundCase{
		// A comma-ok receive, and a range over the channel.
		{fn: "ChanRange", subject: "is ErrF1", truth: true, wantApart: true},
		{fn: "ChanCommaOK", subject: "is ErrF1", truth: true, wantApart: true},
		// The function returns a composite the store filled, and reads
		// nothing itself.
		{fn: "WrapperFilled", subject: "is ErrF2", truth: true, wantApart: true},
		{fn: "WrapperFilledFar", subject: "is ErrF2", truth: true, wantApart: true},
		{fn: "ErrorMethodStore", subject: "is ErrF3", truth: true, wantApart: true},
		{fn: "JoinSlice", subject: "is ErrF4", truth: true, wantUnsure: true},
		{fn: "MapWhole", subject: "is ErrF5", truth: true, wantApart: true},
		// The value comes in an argument.
		{fn: "ArgRoute", subject: "is ErrF6", truth: true, wantApart: true},
		// A value that reflection makes of a type chosen at run time,
		// which may equal the Sentinel or have the Concrete Type, stored
		// by reflection into an interface variable and read back by
		// typed code.
		{fn: "MadeSentinel", subject: "is ErrF7", truth: true, wantApart: true},
		{fn: "MadeConcrete", subject: "as CT8", truth: true, wantApart: true},
		// The function reads what the store wrote through reflection:
		// from a reflect.Value made of an interface-typed parameter,
		// also by methods of reflect.Value called through an interface,
		// and from one of memory that package reflect made (by
		// Interface, MapRange, Recv, Index; handed over in a struct).
		{fn: "AnyRead", subject: "is ErrR1", truth: true, wantApart: true},
		{fn: "DynRead", subject: "is ErrR8", truth: true, wantApart: true},
		{fn: "FreshRead", subject: "is ErrR2", truth: true, wantApart: true},
		{fn: "MapRangeRead", subject: "is ErrR3", truth: true, wantApart: true},
		{fn: "RecvRead", subject: "is ErrR4", truth: true, wantApart: true},
		{fn: "IndexRead", subject: "is ErrR5", truth: true, wantApart: true},
		{fn: "StructRead", subject: "is ErrR6", truth: true, wantApart: true},
		// The second of two stores writes what the function reads.
		{fn: "TwoStage", subject: "is ErrR7", truth: true, wantApart: true},
		// A recorded store that cannot matter: the function reads no
		// memory, or only a variable whose address is never taken.
		{fn: "Clean", subject: "is ErrF1", wantNo: true},
		{fn: "QuietVar", subject: "is ErrF1", wantNo: true},
		{fn: "Clean", subject: "is ErrF5", wantNo: true},
		{fn: "Clean", subject: "is ErrF7", wantNo: true},
		{fn: "QuietVar", subject: "as CT8", wantNo: true},
		{fn: "Clean", subject: "is ErrR1", wantNo: true},
		{fn: "QuietVar", subject: "is ErrR2", wantNo: true},
	})
}

// TestVerdictSoundCalls does the same for a call that only a store
// through reflection makes possible: a dynamic call on a value of a type
// that reaches it through the store alone, and a call through
// reflect.Value.Call of a function with a slice parameter that is also
// called statically. The flooding pass follows the first over the CHA
// callees of the call, and reads the slice parameter of the second as
// memory of its element type.
func TestVerdictSoundCalls(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_calls.txtar", []soundCase{
		{fn: "RegOp", subject: "is ErrC1", truth: true, wantApart: true},
		{fn: "Causer", subject: "is ErrC2", truth: true, wantApart: true},
		{fn: "VaReflectCall", subject: "is ErrC4", truth: true, wantApart: true},
		{fn: "SliceReflectCall", subject: "is ErrC6", truth: true, wantApart: true},
		// Without a store: the argument of Value.Call reaches the
		// elements of the variadic parameter.
		{fn: "VaReflectCallDirect", subject: "is ErrC5", truth: true, wantMaybe: true},
		// A recorded store, and nothing it could matter to.
		{fn: "Clean", subject: "is ErrC1", wantNo: true},
		{fn: "Clean", subject: "is ErrC4", wantNo: true},
	})
}

// TestVerdictSoundSprint does the same for a call inside package fmt
// that only a store through reflection brings its callee to: the Error
// method of the value that fmt.Sprint is passed. The program has this
// one case, so that nothing else leads the flooding pass to the method.
func TestVerdictSoundSprint(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_sprint.txtar", []soundCase{
		{fn: "SprintValue", subject: "is ErrS1", truth: true, wantApart: true},
	})
}

// TestFloodUpperNil pins what a Membership Query does when its
// FloodUpper is left out: the flooding pass has the wide call edges all
// the same (RegOp is a plain No with those of the upward pass, see
// narrowEdges), and where they cannot be derived the Query fails.
func TestFloodUpperNil(t *testing.T) {
	t.Parallel()
	p := loadProgram(t, "sound_calls.txtar")
	subj := p.subject(t, "is ErrC1")
	m, err := p.membershipWith(t, "RegOp", subj, func(o *query.MembershipOptions) { o.FloodUpper = nil })
	if err != nil {
		t.Fatal(err)
	}
	if !m.FloodRan || !m.ApartFromReflection() {
		t.Errorf("FloodUpper nil: Verdict is %v, flooding pass ran %v, %d stores listed; want no, apart from reflection", m.Verdict, m.FloodRan, len(m.ReflectStores))
	}
	// The call edges of the upward pass are what the flooding pass must
	// not run with: they give the unsound No.
	m, err = p.membershipWith(t, "RegOp", subj, func(o *query.MembershipOptions) { o.FloodUpper = o.Upper })
	if err != nil {
		t.Fatal(err)
	}
	if m.Verdict != query.No || m.ApartFromReflection() {
		t.Errorf("FloodUpper = Upper: Verdict is %v, apart from reflection %v; the case no longer tells the wide call edges apart", m.Verdict, m.ApartFromReflection())
	}
	// No *resolve.Upper to derive them from.
	_, err = p.membershipWith(t, "RegOp", subj, func(o *query.MembershipOptions) {
		o.Upper, o.FloodUpper = narrowEdges{o.Upper}, nil
	})
	if err == nil {
		t.Error("FloodUpper nil with an Upper that is not a *resolve.Upper: no error")
	}
	_, err = p.membershipWith(t, "RegOp", subj, func(o *query.MembershipOptions) { o.Upper, o.FloodUpper = nil, nil })
	if err == nil {
		t.Error("FloodUpper and Upper nil: no error")
	}
}

// narrowEdges is call edges that are not a *resolve.Upper.
type narrowEdges struct{ c mark.Callees }

func (n narrowEdges) Callees(site ssa.CallInstruction) []*ssa.Function { return n.c.Callees(site) }
func (n narrowEdges) Callers(fn *ssa.Function) []ssa.CallInstruction   { return n.c.Callers(fn) }

// TestFloodingPass pins when the flooding pass runs and what the Verdict
// lists: after a No with recorded stores it runs, and the stores are
// listed if and only if it reaches the Target: all of them, followed by
// the stores that only the flooding pass records (the stores of a value read
// through reflection, here the two of a value that reflection made, and
// the second of two stores in a row); after a Maybe every recorded store
// is listed and it does not run; without a recorded store it does not
// run.
func TestFloodingPass(t *testing.T) {
	t.Parallel()
	p := loadProgram(t, "sound_flood.txtar")
	for _, c := range []struct {
		fn, subject string
		verdict     query.Verdict
		recorded    int
		flood       bool
		listed      int
		// lists is a function with a store that must be listed.
		lists string
	}{
		// Two stores (the two Sends) are recorded for ErrF1.
		{"ChanCommaOK", "is ErrF1", query.No, 2, true, 5, "store1"},
		{"Clean", "is ErrF1", query.No, 2, true, 0, ""},
		{"QuietVar", "is ErrF1", query.No, 2, true, 0, ""},
		// The Subject also reaches the function without reflection.
		{"Direct", "is ErrF2", query.Maybe, 1, false, 1, "store2"},
		// The store is in a method that only fmt calls.
		{"ErrorMethodStore", "is ErrF3", query.No, 1, true, 4, "Error"},
		// The flood does not ask where a store could write: the store of
		// ErrF3 into one wrapper's field counts for the other wrapper.
		{"WrapperFilled", "is ErrF3", query.No, 1, true, 4, "Error"},
		{"Clean", "is ErrF3", query.No, 1, true, 0, ""},
		// The store that writes what the function reads is recorded in
		// the flooding pass only.
		{"TwoStage", "is ErrR7", query.No, 1, true, 4, "store16b"},
		// Nothing recorded.
		{"Clean", "is io.EOF", query.No, 0, false, 0, ""},
	} {
		m := p.membership(t, c.fn, p.subject(t, c.subject))
		if m.Verdict != c.verdict || m.MarkStats.ReflectStores != c.recorded || m.FloodRan != c.flood || len(m.ReflectStores) != c.listed {
			t.Errorf("%s, %s: Verdict %v, %d stores recorded, flooding pass ran %v, %d stores listed; want %v, %d, %v, %d",
				c.fn, c.subject, m.Verdict, m.MarkStats.ReflectStores, m.FloodRan, len(m.ReflectStores), c.verdict, c.recorded, c.flood, c.listed)
		}
		if apart := c.verdict == query.No && c.listed > 0; m.ApartFromReflection() != apart {
			t.Errorf("%s, %s: apart from reflection %v, want %v", c.fn, c.subject, m.ApartFromReflection(), apart)
		} else if apart && len(m.FloodTrace) == 0 {
			t.Errorf("%s, %s: no trace of the flooding pass to the Target", c.fn, c.subject)
		}
		// The recorded stores come first, then those of the flooding
		// pass alone.
		first := 0
		for i, rs := range m.ReflectStores {
			if !rs.FloodOnly {
				first++
				if i > 0 && m.ReflectStores[i-1].FloodOnly {
					t.Errorf("%s, %s: a recorded store is listed after one of the flooding pass", c.fn, c.subject)
				}
			}
		}
		if c.listed > 0 && first != c.recorded {
			t.Errorf("%s, %s: %d listed stores are recorded ones, want %d", c.fn, c.subject, first, c.recorded)
		}
		found := c.lists == ""
		for i, rs := range m.ReflectStores {
			found = found || rs.Fn.Name() == c.lists
			for _, earlier := range m.ReflectStores[:i] {
				if earlier.Site == rs.Site {
					t.Errorf("%s, %s: a store is listed twice", c.fn, c.subject)
				}
			}
		}
		if !found {
			t.Errorf("%s, %s: the store in %s is not listed", c.fn, c.subject, c.lists)
		}
	}
}

// TestVerdictSoundConvert does the same for a pointer Sentinel that the
// program converts to another pointer type and back: where the Marks do
// not follow the pointer, every pointer of the Sentinel's type is a seed.
func TestVerdictSoundConvert(t *testing.T) {
	t.Parallel()
	runSound(t, "sound_convert.txtar", []soundCase{
		{fn: "ConvLocal", subject: "is ErrL1", truth: true, wantUnsure: true},
		{fn: "ConvField", subject: "is ErrL2", truth: true, wantUnsure: true},
		{fn: "ConvNamedPtr", subject: "is ErrL3", truth: true, wantUnsure: true},
		{fn: "ConvAnon", subject: "is ErrL4", truth: true, wantUnsure: true},
		{fn: "ConvTwin", subject: "is ErrL5", truth: true, wantUnsure: true},
		{fn: "ConvIs", subject: "is ErrL6", truth: true, wantUnsure: true},
		{fn: "ConvTwinAssert", subject: "is ErrL7", truth: true, wantUnsure: true},
		{fn: "ConvSlice", subject: "is ErrL8", truth: true, wantUnsure: true},
		// A pointer type the program converts from but never to, and one
		// it does not convert, are the Sentinel's pointer alone.
		{fn: "ConvAway", subject: "is ErrK1", wantNo: true},
		{fn: "ConvNone", subject: "is ErrK2", wantNo: true},
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
		// An Is method that compares a conversion of its target: to a
		// type that carries no Mark, or to another error type.
		{fn: "ConvInt", subject: "is ErrQ1", truth: true},
		{fn: "ConvString", subject: "is ErrQ2", truth: true},
		{fn: "ConvNeq", subject: "is ErrQ3", truth: true},
		{fn: "ConvSwitch", subject: "is ErrQ4", truth: true},
		{fn: "ConvHelper", subject: "is ErrQ5", truth: true},
		{fn: "ConvNamed", subject: "is ErrQ6", truth: true},
		{fn: "ConvStruct", subject: "is ErrQ7", truth: true},
		{fn: "ConvPtr", subject: "is ErrQ8", truth: true},
		// What still excludes: an assertion the Subject fails, and the
		// asserted value compared with an unmarked value of its type.
		{fn: "ConvGuarded", subject: "is ErrQ9", truth: true},
		{fn: "ConvGuarded", subject: "is ErrP2", wantNo: true},
		{fn: "PtrAssert", subject: "is ErrQ10", truth: true},
		{fn: "PtrAssert", subject: "is ErrQ8", wantNo: true},
		// A pointer the program keeps before it becomes the Sentinel.
		{fn: "PtrKept", subject: "is ErrQ11", truth: true},
		{fn: "PtrKeptIs", subject: "is ErrQ11", truth: true},
		{fn: "PtrKept", subject: "is ErrQ13", wantNo: true},
	})
}
