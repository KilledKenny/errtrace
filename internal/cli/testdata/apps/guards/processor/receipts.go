package processor

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"example.com/guards/fsx"
)

// OpenReceipt treats a missing receipt file as fine (errors.Is idiom).
func /*@fsIs*/OpenReceipt(name string) error {
	err := fsx.Open(name)
	if errors.Is(err, fsx.ErrNotExist) {
		return nil
	}
	return err
}

// OpenReceiptLegacy uses the os.IsNotExist-shaped helper.
func /*@fsHelper*/OpenReceiptLegacy(name string) error {
	if err := fsx.Open(name); err != nil && !fsx.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadReceipts reads until EOF; EOF ends the loop and is not an error.
func /*@eofLoop*/ReadReceipts() error {
	for i := 0; ; i++ {
		err := fsx.Read(i)
		if err == fsx.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// StatReceipt uses the real stdlib sentinels on a PathError-wrapped error.
func /*@stdIs*/StatReceipt(name string) error {
	err := fsx.Stat(name)
	if errors.Is(err, fs.ErrNotExist) || err == io.EOF {
		return nil
	}
	return err
}

// StatReceiptLegacy uses os.IsNotExist, which does not unwrap like errors.Is.
func /*@stdHelper*/StatReceiptLegacy(name string) error {
	err := fsx.Stat(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
