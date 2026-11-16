package main

import "example.com/generics/validate"

// checkInferred instantiates Check[UserID] by inference.
func checkInferred(id validate.UserID) error {
	return validate./*@inferred*/Check(id)
}

// checkExplicit instantiates Check[Name] explicitly.
func checkExplicit(n validate.Name) error {
	return validate./*@explicit*/Check[validate.Name](n)
}

// checkEmail instantiates Check[*Email].
func checkEmail(e *validate.Email) error {
	return /*@qualifier*/validate.Check/*@lparen*/(e)
}

// checkIface instantiates Check with the interface type itself; main only
// ever passes a Name and an *Email.
func checkIface(v validate.Validator) error {
	return validate./*@iface*/Check[validate.Validator](v)
}

// newEmail uses the pointer-constraint constructor.
func newEmail(addr string) (*validate.Email, error) {
	return validate./*@ptr*/New[validate.Email](func(e *validate.Email) { e.Addr = addr })
}

// checkLimits instantiates the type-set helpers with int, UserID and float64.
func /*@limits*/checkLimits(age int, id validate.UserID, ratio float64) error {
	if err := validate./*@posint*/Positive(age); err != nil {
		return err
	}
	if err := validate./*@rangeid*/InRange(id, 1, 1000); err != nil {
		return err
	}
	return validate.InRange(ratio, 0, 1)
}
