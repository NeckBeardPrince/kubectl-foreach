// Copyright 2022 Twitter, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

// outputFormat selects how per-context output is presented.
type outputFormat string

const (
	formatStream   outputFormat = "stream" // live, prefixed lines (default)
	formatTable    outputFormat = "table"
	formatCSV      outputFormat = "csv"
	formatMarkdown outputFormat = "markdown"
)

func parseFormat(s string) (outputFormat, error) {
	switch f := outputFormat(strings.ToLower(s)); f {
	case formatStream, formatTable, formatCSV, formatMarkdown:
		return f, nil
	case "md":
		return formatMarkdown, nil
	}
	return "", fmt.Errorf("unknown output format %q (want stream, table, csv, or markdown)", s)
}

// buffered reports whether the format needs all output before rendering.
func (f outputFormat) buffered() bool { return f != formatStream }

// result is the captured stdout of one context.
type result struct {
	ctx string
	out string
}

// bufCloser captures a context's stdout; Close is a no-op so run() can close it.
type bufCloser struct{ bytes.Buffer }

func (*bufCloser) Close() error { return nil }

var multiSpace = regexp.MustCompile(`\s{2,}`)

// splitFields splits one output line into columns. Tab-separated lines (e.g.
// custom-columns or jsonpath with \t) split on tabs; otherwise columns are
// separated by runs of 2+ spaces, which is how kubectl pads its tables.
func splitFields(line string) []string {
	line = strings.TrimRight(line, "\r")
	if strings.Contains(line, "\t") {
		fields := strings.Split(line, "\t")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		return fields
	}
	return multiSpace.Split(strings.TrimSpace(line), -1)
}

// buildRows turns results into a header (nil if none) and rectangular rows,
// each prefixed with its context name. With header set, the first line of each
// context is a header: the first one seen is kept and the rest are dropped.
func buildRows(results []result, header bool) (hdr []string, rows [][]string) {
	var cols int
	for _, r := range results {
		first := true
		for _, line := range strings.Split(r.out, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			fields := splitFields(line)
			if header && first {
				first = false
				if hdr == nil {
					hdr = fields
					cols = max(cols, len(fields))
				}
				continue
			}
			first = false
			rows = append(rows, append([]string{r.ctx}, fields...))
			cols = max(cols, len(fields))
		}
	}
	pad := func(row []string, n int) []string {
		for len(row) < n {
			row = append(row, "")
		}
		return row
	}
	for i := range rows {
		rows[i] = pad(rows[i], cols+1)
	}
	if hdr != nil {
		hdr = pad(append([]string{"CONTEXT"}, hdr...), cols+1)
	}
	return hdr, rows
}

// render writes results in the requested buffered format.
func render(w io.Writer, f outputFormat, header bool, results []result) error {
	hdr, rows := buildRows(results, header)
	if len(rows) == 0 {
		return nil
	}
	switch f {
	case formatCSV:
		return renderCSV(w, hdr, rows)
	case formatMarkdown:
		return renderMarkdown(w, hdr, rows)
	case formatTable:
		return renderTable(w, hdr, rows)
	}
	return fmt.Errorf("format %q cannot be rendered from buffered output", f)
}

func renderCSV(w io.Writer, hdr []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	if hdr != nil {
		if err := cw.Write(hdr); err != nil {
			return err
		}
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

func renderMarkdown(w io.Writer, hdr []string, rows [][]string) error {
	if hdr == nil { // GFM tables need a header row
		hdr = []string{"CONTEXT"}
		for i := 1; i < len(rows[0]); i++ {
			hdr = append(hdr, fmt.Sprintf("COL%d", i))
		}
	}
	line := func(cells []string) string {
		esc := make([]string, len(cells))
		for i, c := range cells {
			esc[i] = strings.ReplaceAll(c, "|", `\|`)
		}
		return "| " + strings.Join(esc, " | ") + " |\n"
	}
	var b strings.Builder
	b.WriteString(line(hdr))
	b.WriteString("|" + strings.Repeat(" --- |", len(hdr)) + "\n")
	for _, r := range rows {
		b.WriteString(line(r))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func renderTable(w io.Writer, hdr []string, rows [][]string) error {
	cell := lipgloss.NewStyle().Padding(0, 1)
	bold := cell.Bold(true)
	t := table.New().
		Border(lipgloss.NormalBorder()).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return bold
			}
			return cell
		}).
		Rows(rows...)
	if hdr != nil {
		t = t.Headers(hdr...)
	}
	_, err := fmt.Fprintln(w, t.String())
	return err
}
