package processor

import (
	"errors"
	"fmt"
	"log"
	"sync"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// SettleDeferred clears a declined error in a deferred function.
func /*@deferClear*/SettleDeferred(n int) (err error) {
	defer func() {
		if errors.Is(err, payerr.ErrDeclined) {
			err = nil
		}
	}()
	return gateway.Charge(n)
}

// SettleDeferredTranslate replaces a timeout in a deferred function.
func /*@deferTranslate*/SettleDeferredTranslate(n int) (err error) {
	defer func() {
		if err == payerr.ErrTimeout {
			err = fmt.Errorf("settle: %w", payerr.ErrInternal)
		}
	}()
	err = gateway.Charge(n)
	return err
}

// SettleDeferredLog only logs in the deferred function.
func /*@deferLog*/SettleDeferredLog(n int) (err error) {
	defer func() {
		if err == payerr.ErrDeclined {
			log.Print("declined")
		}
	}()
	return gateway.Void(n)
}

// VoidWatched checks the error in a goroutine that only logs.
func /*@goLog*/VoidWatched(n int) error {
	err := gateway.Void(n)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err == payerr.ErrDeclined {
			log.Print("declined")
		}
	}()
	wg.Wait()
	return err
}

// VoidAsync guards in the goroutine and sends the result over a channel.
func /*@goChan*/VoidAsync(n int) error {
	errc := make(chan error, 1)
	go func() {
		err := gateway.Void(n)
		if err == payerr.ErrDeclined {
			errc <- nil
			return
		}
		errc <- err
	}()
	return <-errc
}

// VoidAsyncClear lets a goroutine clear the captured error.
func /*@goClear*/VoidAsyncClear(n int) error {
	err := gateway.Void(n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err == payerr.ErrDeclined {
			err = nil
		}
	}()
	<-done
	return err
}
