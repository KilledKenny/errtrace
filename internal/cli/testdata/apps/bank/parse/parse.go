// Package parse turns external input (strings, JSON) into domain values.
package parse

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"example.com/bank/domain"
)

// ErrBadDate is wrapped by When for unparseable timestamps.
var ErrBadDate = errors.New("parse: bad date")

// Amount parses a positive amount in minor units.
func /*@amount*/Amount(s string) (domain.Money, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, &domain.ValidationError{Field: "amount", Reason: "too large"}
		}
		return 0, fmt.Errorf("amount %q: %w", s, err)
	}
	if n <= 0 {
		return 0, &domain.ValidationError{Field: "amount", Reason: "must be positive"}
	}
	return domain.Money(n), nil
}

// Date accepts a few layouts; the individual time.Parse errors are dropped.
func /*@date*/Date(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, &domain.ValidationError{Field: "at", Reason: "unrecognised date"}
}

// When parses a strict RFC 3339 timestamp and replaces *time.ParseError.
func /*@when*/When(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		var pe *time.ParseError
		if errors.As(err, &pe) {
			return time.Time{}, fmt.Errorf("%w: near %q", ErrBadDate, pe.ValueElem)
		}
		return time.Time{}, err
	}
	return t, nil
}

type wireTransfer struct {
	ID       int64  `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	At       string `json:"at"`
}

// Request decodes the JSON body of a transfer request. Decoder errors never
// leave this function: they are turned into a message or a ValidationError.
func /*@request*/Request(data []byte) (domain.Transfer, error) {
	var w wireTransfer
	if err := json./*@unmarshal_call*/Unmarshal(data, &w); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return domain.Transfer{}, fmt.Errorf("parse: malformed JSON at offset %d", se.Offset)
		}
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return domain.Transfer{}, &domain.ValidationError{Field: te.Field, Reason: "wrong JSON type"}
		}
		return domain.Transfer{}, fmt.Errorf("parse: %v", err)
	}
	return w.build()
}

func (w wireTransfer) /*@build*/build() (domain.Transfer, error) {
	amt, err := /*@amount_call*/Amount(w.Amount)
	if err != nil {
		return domain.Transfer{}, err
	}
	at, err := Date(w.At)
	if err != nil {
		return domain.Transfer{}, err
	}
	return domain.Transfer{
		ID: domain.TransferID(w.ID), From: domain.AccountID(w.From), To: domain.AccountID(w.To),
		Amount: amt, Currency: w.Currency, At: at,
	}, nil
}

// Line parses the CLI form "FROM TO AMOUNT [RFC3339]".
func /*@line*/Line(id int64, fields []string) (domain.Transfer, error) {
	if len(fields) < 3 {
		return domain.Transfer{}, &domain.ValidationError{Field: "line", Reason: "want FROM TO AMOUNT"}
	}
	amt, err := Amount(fields[2])
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("transfer %d: %w", id, err)
	}
	at := time.Now()
	if len(fields) > 3 {
		if at, err = /*@when_call*/When(fields[3]); err != nil {
			return domain.Transfer{}, fmt.Errorf("transfer %d: %w", id, err)
		}
	}
	return domain.Transfer{
		ID: domain.TransferID(id), From: domain.AccountID(fields[0]), To: domain.AccountID(fields[1]),
		Amount: amt, Currency: "EUR", At: at,
	}, nil
}
