// Package records decodes the data rows of CSV and JSON-lines inputs.
package records

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrNoRows is returned for an input without any data row.
var ErrNoRows = errors.New("records: no rows")

// RowError locates a failure in the input.
type RowError struct {
	Row int
	Err error
}

func (e *RowError) Error() string { return fmt.Sprintf("row %d: %v", e.Row, e.Err) }
func (e *RowError) Unwrap() error { return e.Err }

// Row is one imported record.
type Row struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ReadCSV decodes "name,count" rows. csv.ErrFieldCount is tolerated (the
// row is skipped); every other failure is reported with its row number.
func /*@readcsv*/ReadCSV(doc string) ([]Row, error) {
	r := csv.NewReader(strings.NewReader(doc))
	var rows []Row
	for i := 1; ; i++ {
		rec, err := r./*@csvread*/Read()
		if err == io.EOF {
			break
		}
		if errors.Is(err, csv.ErrFieldCount) {
			continue
		}
		if err != nil {
			return nil, &RowError{Row: i, Err: err}
		}
		n, err := strconv.Atoi(rec[1])
		if err != nil {
			return nil, &RowError{Row: i, Err: err}
		}
		rows = append(rows, Row{Name: rec[0], Count: n})
	}
	if len(rows) == 0 {
		return nil, ErrNoRows
	}
	return rows, nil
}

// ReadJSONLines decodes a stream of JSON objects.
func /*@readjson*/ReadJSONLines(r io.Reader) ([]Row, error) {
	dec := json.NewDecoder(r)
	var rows []Row
	for {
		var row Row
		err := dec./*@decode*/Decode(&row)
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return rows, fmt.Errorf("records: json: %w", err)
		}
		rows = append(rows, row)
	}
}

// Encode renders rows as JSON.
func /*@encode*/Encode(rows []Row) ([]byte, error) {
	return json./*@marshal*/Marshal(rows)
}
