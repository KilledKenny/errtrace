package dispatch

import (
	"example.com/plugins/core"
	"example.com/plugins/drivers"
)

// LegacySend is kept for an old API; nothing calls it any more.
func /*@legacy*/LegacySend(d core.Driver, h core.Handler, e core.Event) error {
	if err := h.Handle(e); err != nil {
		return err
	}
	return d./*@legacy_send*/Send(e)
}

// debugCarrier is a leftover debugging helper, also never called. It is
// the only place that turns a Carrier into a core.Driver.
func /*@debugcarrier*/debugCarrier(e core.Event) error {
	var d core.Driver = drivers.Carrier{}
	return d.Send(e)
}
