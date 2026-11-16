// Package dispatch routes events to drivers.
package dispatch

import "example.com/plugins/core"

// The Send* functions are the delivery tiers. main feeds each of them a
// fixed number of driver types.

// SendDirect is only ever given an Email.
func /*@direct*/SendDirect(d core.Driver, e core.Event) error { return d.Send(e) }

// SendPair is given an Email and an *SMS.
func /*@pair*/SendPair(d core.Driver, e core.Event) error { return d.Send/*@pair_lparen*/(e) }

// SendEither is given an Email and an *Email.
func /*@either*/SendEither(d core.Driver, e core.Event) error { return d.Send(e) }

// SendSeven is given seven failing drivers.
func /*@seven*/SendSeven(d core.Driver, e core.Event) error { return d.Send(e) }

// SendEight is given eight failing drivers.
func /*@eight*/SendEight(d core.Driver, e core.Event) error { return d.Send(e) }

// SendQuiet is given seven failing drivers and Log, which never fails.
func /*@quiet*/SendQuiet(d core.Driver, e core.Event) error { return d.Send(e) }

// SendNine is given eight failing drivers and Log.
func /*@nine*/SendNine(d core.Driver, e core.Event) error { return d./*@nine_call*/Send(e) }

// SendWrapped is given drivers whose Send is a promoted method.
func /*@wrapped*/SendWrapped(d core.Driver, e core.Event) error { return d.Send(e) }

// SendAudited is only ever given an Audited, whose own Send shadows the
// one promoted from the Driver it embeds.
func /*@audited*/SendAudited(d core.Driver, e core.Event) error { return d.Send(e) }
