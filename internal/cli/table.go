package cli

import (
	"io"
	"strings"
	"text/tabwriter"
)

func renderTable(output io.Writer, headers []string, rows [][]string) error {
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	if len(headers) > 0 {
		if err := renderTableRow(writer, headers); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := renderTableRow(writer, row); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func renderTableRow(output io.Writer, cells []string) error {
	sanitized := make([]string, 0, len(cells))
	for _, cell := range cells {
		sanitized = append(sanitized, sanitizeTableCell(cell))
	}
	_, err := io.WriteString(output, strings.Join(sanitized, "\t")+"\n")
	return err
}

func sanitizeTableCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
