package cli

import (
	"io"
	"strings"
)

func renderTable(output io.Writer, headers []string, rows [][]string) error {
	widths := tableRenderWidths(headers, rows)
	if len(headers) > 0 {
		if err := renderTableRow(output, headers, widths); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := renderTableRow(output, row, widths); err != nil {
			return err
		}
	}
	return nil
}

func renderTableRow(output io.Writer, cells []string, widths []int) error {
	sanitized := make([]string, 0, len(cells))
	for _, cell := range cells {
		sanitized = append(sanitized, sanitizeTableCell(cell))
	}
	for i, width := range widths {
		cell := ""
		if i < len(sanitized) {
			cell = sanitized[i]
		}
		if _, err := io.WriteString(output, cell); err != nil {
			return err
		}
		if i == len(widths)-1 {
			continue
		}
		padding := width - displayWidth(cell) + 2
		if _, err := io.WriteString(output, strings.Repeat(" ", padding)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(output, "\n")
	return err
}

func tableRenderWidths(headers []string, rows [][]string) []int {
	columnCount := len(headers)
	for _, row := range rows {
		columnCount = max(columnCount, len(row))
	}
	widths := make([]int, columnCount)
	for i, header := range headers {
		widths[i] = displayWidth(sanitizeTableCell(header))
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], displayWidth(sanitizeTableCell(cell)))
		}
	}
	return widths
}

func sanitizeTableCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
