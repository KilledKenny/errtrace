// Command guards runs every payment handler once and prints what failed.
package main

import (
	"fmt"
	"os"

	"example.com/guards/payerr"
	"example.com/guards/processor"
	"example.com/guards/runner"
)

func main() {
	n := len(os.Args)
	lenient := n > 2
	p := &processor.Processor{Lenient: lenient}
	r := &runner.Runner{Max: 3}

	steps := []error{
		processor.ChargeIgnoreDeclined(n),
		processor.ChargeNeqElse(n),
		processor.LookupEqWrapped(n),
		processor.ChargeOnlyDeclined(n),
		processor.ChargeElseChain(n),
		processor.ChargeSwitch(n),
		processor.ChargeSwitchKeep(n),
		processor.ChargeAliasCopy(n),
		processor.ChargeVoidOther(n),
		processor.ChargeThenVoid(n),
		processor.ChargeSameAsFirst(n),
		processor.ChargeWrapThenEq(n),
		processor.ValidateSentinels(n),
		processor.PrecheckTypedNil(n),
		processor.PrecheckNilCheck(n),
		processor.PrecheckInlineNilCheck(n),

		processor.LookupIs(n),
		processor.LookupNotIs(n),
		processor.LookupAndChain(n),
		processor.LookupOr(n, lenient),
		processor.LookupAndBudget(n, 3-n),
		processor.LookupNested(n, lenient),
		processor.RefundIsJoined(n),
		processor.RefundJoinMaybe(n),
		processor.LookupTranslate(n),
		processor.LookupRewrap(n),
		processor.LookupLog(n),
		processor.LookupSwapOnIs(n),
		processor.LookupSwitchTrue(n),
		processor.LookupUnwrapEq(n),
		processor.QuirkyIs(n),
		processor.LookupOrRewrap(n),

		processor.ChargeAs(n),
		processor.ChargeAsCode(n),
		processor.LookupAsTemporary(n),
		processor.CaptureAs(n),
		processor.CaptureAsOp(n),
		processor.QuirkyAs(n),
		processor.ChargeCommaOk(n),
		processor.CaptureCommaOk(n),
		processor.ChargeMustCode(n),
		processor.ChargeTypeSwitch(n),
		processor.LookupTypeSwitch(n),
		processor.CaptureTypeSwitch(n),

		processor.LookupHelper(n),
		p.Charge(n),
		processor.LookupClassified(n),
		processor.CaptureClassified(n),
		processor.ChargeClassifiedTwice(n),
		processor.VoidPassThrough(n, lenient),

		processor.SettleDeferred(n),
		processor.SettleDeferredTranslate(n),
		processor.SettleDeferredLog(n),
		processor.VoidWatched(n),
		processor.VoidAsync(n),
		processor.VoidAsyncClear(n),

		processor.OpenReceipt(os.Args[0]),
		processor.OpenReceiptLegacy(os.Args[0]),
		processor.ReadReceipts(),
		processor.StatReceipt(os.Args[0]),
		processor.StatReceiptLegacy(os.Args[0]),

		r.Run("job-1"),
		r.RunSkipTimeouts(),
		r.RunUntilSettled(),
		r.RunLast(),
		r.RunTimeoutsOnly("job-2"),

		processor.ChargeAsWrongTarget(n),
		processor.LookupIsTarget(n, payerr.ErrRateLimited),
		processor.LookupIsFlag(n),
		processor.LookupIsAlias(n),
		processor.PrecheckOnlyTypedNil(n),
		processor.PrecheckInlineOnlyTypedNil(n),
		processor.VoidDescribe(n),
		processor.ChargeClosureGuard(n),
		processor.ChargeIfVoidDeclined(n),
	}
	failed := 0
	for i, err := range steps {
		if err != nil {
			fmt.Fprintf(os.Stderr, "step %d: %v\n", i, err)
			failed++
		}
	}
	if /*@mainIf*/failed > 0 {
		os.Exit(1)
	}
}
