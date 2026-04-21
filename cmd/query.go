package cmd

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

const (
	formatTable = "table"
	formatTSV   = "tsv"
	formatCSV   = "csv"
)

func newQueryCmd() *cobra.Command {
	var format string
	var noHeader bool

	cmd := &cobra.Command{
		Use:   "query [SQL]",
		Short: "Run an ad-hoc SQL query against the database",
		Long: `Run a SQL query against the winnow database.

If no SQL argument is given, the query is read from stdin. Custom SQL
functions registered by winnow (e.g. human_size) are available.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var query string
			if len(args) == 1 {
				query = args[0]
			} else {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("reading stdin: %w", err)
				}
				query = string(data)
			}
			if strings.TrimSpace(query) == "" {
				return errors.New("no query provided")
			}
			return runQuery(query, format, !noHeader)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", formatTable, "output format: table, tsv, csv")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "omit column headers")

	return cmd
}

func runQuery(query, format string, header bool) error {
	_, database, err := openBootstrapDB()
	if err != nil {
		return err
	}
	defer database.Close()

	rows, err := database.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}

	switch format {
	case formatTable:
		return writeTable(os.Stdout, rows, cols, header)
	case formatCSV:
		return writeCSV(os.Stdout, rows, cols, header)
	case formatTSV:
		return writeTSV(os.Stdout, rows, cols, header)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

func scanRow(rows *sql.Rows, ncols int) ([]string, error) {
	vals := make([]any, ncols)
	ptrs := make([]any, ncols)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make([]string, ncols)
	for i, v := range vals {
		out[i] = formatValue(v)
	}
	return out, nil
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

// writeTable buffers all rows via tabwriter to compute column widths.
// Streaming is not possible for aligned output.
func writeTable(w io.Writer, rows *sql.Rows, cols []string, header bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if header {
		fmt.Fprintln(tw, strings.Join(cols, "\t"))
		sep := make([]string, len(cols))
		for i, c := range cols {
			sep[i] = strings.Repeat("-", len(c))
		}
		fmt.Fprintln(tw, strings.Join(sep, "\t"))
	}
	for rows.Next() {
		row, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tw.Flush()
}

func writeCSV(w io.Writer, rows *sql.Rows, cols []string, header bool) error {
	cw := csv.NewWriter(w)
	if header {
		if err := cw.Write(cols); err != nil {
			return err
		}
	}
	for rows.Next() {
		row, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// writeTSV emits plain tab-separated output. TSV has no escape mechanism,
// so callers should avoid querying fields that may contain tabs or newlines.
func writeTSV(w io.Writer, rows *sql.Rows, cols []string, header bool) error {
	bw := bufio.NewWriter(w)
	if header {
		if _, err := fmt.Fprintln(bw, strings.Join(cols, "\t")); err != nil {
			return err
		}
	}
	for rows.Next() {
		row, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(bw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return bw.Flush()
}
