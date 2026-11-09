// Package flow is the intraprocedural analysis of one SSA function: which of
// its results are error results and where the error values they carry come
// from.
package flow

import "go/types"

// errorIface is the underlying interface of the predeclared error type.
var errorIface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// IsErrorType reports whether t can carry an error: it is error, or it
// implements error (directly, or through its pointer type *T when t is not
// itself a pointer or an interface). A function result of such a type is an
// error result, the place an Error Set describes.
func IsErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	if types.Implements(t, errorIface) {
		return true
	}
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Interface:
		return false
	}
	if _, isParam := t.(*types.TypeParam); isParam {
		return false
	}
	return types.Implements(types.NewPointer(t), errorIface)
}

// ErrorResults returns the indices of the results of sig that are error
// results (see IsErrorType).
func ErrorResults(sig *types.Signature) []int {
	var idx []int
	for i := range sig.Results().Len() {
		if IsErrorType(sig.Results().At(i).Type()) {
			idx = append(idx, i)
		}
	}
	return idx
}
