// Package fmtverb parses fmt format strings the way fmt.Errorf does at run
// time, so that errtrace can tell which operands of a fmt.Errorf call a
// %w verb makes part of a Wrap Chain. It follows the printer in
// fmt/print.go (doPrintf, argNumber, intFromArg): flags, explicit argument
// indexes such as %[2]w and the index reset that follows them, '*' widths
// and precisions (which use up an operand), %% and malformed verbs.
package fmtverb

import "unicode/utf8"

// Verb is one formatting directive of a format string.
type Verb struct {
	// Verb is the verb rune ('w', 'v', 'd', ...), '%' for "%%", or
	// utf8.RuneError when the format ends before the verb.
	Verb rune
	// Arg is the 0-based index of the operand the verb formats, or -1 if
	// it formats none ("%%", a bad index or a missing operand).
	Arg int
	// BadIndex reports an explicit argument index that is malformed or out
	// of range, or misplaced (as in "%[3]2d"): fmt prints %!v(BADINDEX).
	BadIndex bool
	// Missing reports that no operand was left for the verb: fmt prints
	// %!v(MISSING).
	Missing bool
	// NoVerb reports that the format ends inside the directive: fmt
	// prints %!(NOVERB).
	NoVerb bool
	// Start and End are the byte offsets of the directive in the format.
	Start, End int
}

// Parse returns the directives of format, given the number of operands the
// call passes (numArgs < 0 means any number). The operand indexes are the
// ones fmt.Errorf uses at run time.
func Parse(format string, numArgs int) []Verb {
	if numArgs < 0 {
		numArgs = int(^uint(0) >> 1)
	}
	var verbs []Verb
	end := len(format)
	argNum := 0
	afterIndex := false
	for i := 0; i < end; {
		goodArgNum := true
		for i < end && format[i] != '%' {
			i++
		}
		if i >= end {
			break
		}
		start := i
		i++
		// Flags.
		for i < end && isFlag(format[i]) {
			i++
		}
		// An explicit argument index.
		argNumber := func() {
			if i >= end || format[i] != '[' {
				afterIndex = false
				return
			}
			index, wid, ok := parseArgNumber(format[i:])
			i += wid
			if ok && 0 <= index && index < numArgs {
				argNum, afterIndex = index, true
				return
			}
			goodArgNum = false
			afterIndex = ok
		}
		argNumber()
		// Width.
		if i < end && format[i] == '*' {
			i++
			if argNum < numArgs {
				argNum++
			}
			afterIndex = false
		} else {
			present := false
			present, i = parseNum(format, i, end)
			if afterIndex && present { // "%[3]2d"
				goodArgNum = false
			}
		}
		// Precision.
		if i+1 < end && format[i] == '.' {
			i++
			if afterIndex { // "%[3].2d"
				goodArgNum = false
			}
			argNumber()
			if i < end && format[i] == '*' {
				i++
				if argNum < numArgs {
					argNum++
				}
				afterIndex = false
			} else {
				_, i = parseNum(format, i, end)
			}
		}
		if !afterIndex {
			argNumber()
		}
		if i >= end {
			verbs = append(verbs, Verb{Verb: utf8.RuneError, Arg: -1, NoVerb: true, Start: start, End: end})
			break
		}
		verb, size := rune(format[i]), 1
		if verb >= utf8.RuneSelf {
			verb, size = utf8.DecodeRuneInString(format[i:])
		}
		i += size
		v := Verb{Verb: verb, Arg: -1, Start: start, End: i}
		switch {
		case verb == '%':
		case !goodArgNum:
			v.BadIndex = true
		case argNum >= numArgs:
			v.Missing = true
		default:
			v.Arg = argNum
			argNum++
		}
		verbs = append(verbs, v)
	}
	return verbs
}

// Wrapped returns the operand indexes that the %w verbs of verbs wrap,
// sorted and without duplicates, as fmt.Errorf collects them.
func Wrapped(verbs []Verb) []int {
	var out []int
	for _, v := range verbs {
		if v.Verb != 'w' || v.Arg < 0 {
			continue
		}
		at := 0
		for at < len(out) && out[at] < v.Arg {
			at++
		}
		if at < len(out) && out[at] == v.Arg {
			continue
		}
		out = append(out, 0)
		copy(out[at+1:], out[at:])
		out[at] = v.Arg
	}
	return out
}

func isFlag(c byte) bool {
	switch c {
	case '#', '0', '+', '-', ' ':
		return true
	}
	return false
}

// parseNum skips the decimal number at s[start:end], as fmt's parsenum
// does, and reports whether there was one.
func parseNum(s string, start, end int) (present bool, newi int) {
	if start >= end {
		return false, end
	}
	n := 0
	for newi = start; newi < end && '0' <= s[newi] && s[newi] <= '9'; newi++ {
		if n > 1e6 {
			return false, end // fmt's tooLarge: an overflowing number
		}
		n = n*10 + int(s[newi]-'0')
		present = true
	}
	return present, newi
}

// parseArgNumber is fmt's parseArgNumber: the 0-based value of the
// bracketed number at format[0], the number of bytes to skip, and whether
// it parsed.
func parseArgNumber(format string) (index int, wid int, ok bool) {
	if len(format) < 3 {
		return 0, 1, false
	}
	for i := 1; i < len(format); i++ {
		if format[i] == ']' {
			n := 0
			j := 1
			present := false
			for ; j < i && '0' <= format[j] && format[j] <= '9'; j++ {
				if n > 1e6 {
					return 0, i + 1, false
				}
				n = n*10 + int(format[j]-'0')
				present = true
			}
			if !present || j != i {
				return 0, i + 1, false
			}
			return n - 1, i + 1, true
		}
	}
	return 0, 1, false
}
