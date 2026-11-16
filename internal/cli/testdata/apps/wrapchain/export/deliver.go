package export

import (
	"errors"
	"fmt"

	"example.com/wrapchain/xerr"

	"example.com/wrapchain/doc"
)

// The delivery ladder: every level adds exactly one Wrap around ErrDisk.

func commit() error { return doc.ErrDisk }

func /*@l1*/fsync() error { return fmt.Errorf("fsync: %w", commit()) }

func /*@l2*/writeBlock() error { return xerr.WithOp("block", fsync()) }

func /*@l3*/writeSegment() error { return errors.Join(writeBlock()) }

func /*@l4*/writeFile() error { return fmt.Errorf("file: %w", writeSegment()) }

func /*@l5*/writeDir() error { return &xerr.OpError{Op: "dir", Err: writeFile()} }

func /*@l6*/publish() error { return xerr.Wrapf(writeDir(), "publish %d", 6) }

func /*@l7*/upload() error { return xerr.Annotatef("upload: %w", publish()) }

// Deliver is 8 Wraps away from the origin: the default -chain-depth.
func /*@l8*/Deliver() error { return fmt.Errorf("deliver: %w", upload()) }

// Release is 9 Wraps away.
func /*@l9*/Release() error { return fmt.Errorf("release: %w", Deliver()) }

// Rollout is 10 Wraps away.
func /*@l10*/Rollout() error { return fmt.Errorf("rollout: %w", Release()) }
