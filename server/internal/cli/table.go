package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// table prints aligned label and value rows for text output.
type table struct{ tw *tabwriter.Writer }

func newTable(w io.Writer) *table {
	return &table{tw: tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)}
}

// row prints a row; empty values are skipped so optional fields do not
// leave blank lines.
func (t *table) row(label string, value any) {
	s := fmt.Sprint(value)
	if value == nil || s == "" {
		return
	}
	_, _ = fmt.Fprintf(t.tw, "%s\t%s\n", label, s)
}

func (t *table) flush() error { return t.tw.Flush() }
