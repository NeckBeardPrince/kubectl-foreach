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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_parseFormat(t *testing.T) {
	for in, want := range map[string]outputFormat{
		"stream": formatStream, "TABLE": formatTable, "csv": formatCSV,
		"markdown": formatMarkdown, "md": formatMarkdown,
	} {
		got, err := parseFormat(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := parseFormat("json")
	assert.ErrorContains(t, err, `unknown output format "json"`)
	assert.False(t, formatStream.buffered())
	assert.True(t, formatCSV.buffered())
}

func Test_splitFields(t *testing.T) {
	// tab separated keeps inner spaces, trims padding
	assert.Equal(t, []string{"ns/web", "a.example.test b.example.test"}, splitFields("ns/web\t a.example.test b.example.test"))
	// empty trailing tab field is preserved
	assert.Equal(t, []string{"ns/web", ""}, splitFields("ns/web\t"))
	// kubectl-style padded columns
	assert.Equal(t, []string{"NAME", "READY", "STATUS"}, splitFields("NAME     READY   STATUS"))
	// single spaces stay inside one column
	assert.Equal(t, []string{"two words", "x"}, splitFields("two words   x"))
	assert.Equal(t, []string{"a"}, splitFields("a\r"))
}

var sampleResults = []result{
	{ctx: "ctx-a", out: "NS\tNAME\nweb\tfrontend\ndb\tpostgres\n"},
	{ctx: "ctx-b", out: "NS\tNAME\nweb\tfrontend\n\n"},
	{ctx: "ctx-c", out: ""},
}

func Test_buildRows(t *testing.T) {
	t.Run("header kept once", func(t *testing.T) {
		hdr, rows := buildRows(sampleResults, true)
		assert.Equal(t, []string{"CONTEXT", "NS", "NAME"}, hdr)
		assert.Equal(t, [][]string{
			{"ctx-a", "web", "frontend"},
			{"ctx-a", "db", "postgres"},
			{"ctx-b", "web", "frontend"},
		}, rows)
	})
	t.Run("no header treats first line as data", func(t *testing.T) {
		hdr, rows := buildRows(sampleResults, false)
		assert.Nil(t, hdr)
		assert.Len(t, rows, 5)
		assert.Equal(t, []string{"ctx-a", "NS", "NAME"}, rows[0])
	})
	t.Run("ragged rows are padded", func(t *testing.T) {
		_, rows := buildRows([]result{{ctx: "c", out: "a\tb\tc\nx\n"}}, false)
		assert.Equal(t, [][]string{{"c", "a", "b", "c"}, {"c", "x", "", ""}}, rows)
	})
}

func Test_render(t *testing.T) {
	t.Run("csv", func(t *testing.T) {
		var b bytes.Buffer
		require.NoError(t, render(&b, formatCSV, true, sampleResults))
		assert.Equal(t, "CONTEXT,NS,NAME\nctx-a,web,frontend\nctx-a,db,postgres\nctx-b,web,frontend\n", b.String())
	})
	t.Run("csv quotes commas and quotes", func(t *testing.T) {
		var b bytes.Buffer
		require.NoError(t, render(&b, formatCSV, false, []result{{ctx: "c", out: "a,b\t\"q\"\n"}}))
		assert.Equal(t, "c,\"a,b\",\"\"\"q\"\"\"\n", b.String())
	})
	t.Run("markdown", func(t *testing.T) {
		var b bytes.Buffer
		require.NoError(t, render(&b, formatMarkdown, true, sampleResults))
		assert.Equal(t, "| CONTEXT | NS | NAME |\n| --- | --- | --- |\n| ctx-a | web | frontend |\n| ctx-a | db | postgres |\n| ctx-b | web | frontend |\n", b.String())
	})
	t.Run("markdown without header generates one and escapes pipes", func(t *testing.T) {
		var b bytes.Buffer
		require.NoError(t, render(&b, formatMarkdown, false, []result{{ctx: "c", out: "a|b\tz\n"}}))
		assert.Equal(t, "| CONTEXT | COL1 | COL2 |\n| --- | --- | --- |\n| c | a\\|b | z |\n", b.String())
	})
	t.Run("table", func(t *testing.T) {
		var b bytes.Buffer
		require.NoError(t, render(&b, formatTable, true, sampleResults))
		out := b.String()
		for _, want := range []string{"CONTEXT", "NS", "NAME", "ctx-a", "postgres", "ctx-b"} {
			assert.Contains(t, out, want)
		}
		assert.Contains(t, out, "┌", "bordered table")
	})
	t.Run("no rows prints nothing", func(t *testing.T) {
		var b bytes.Buffer
		require.NoError(t, render(&b, formatCSV, true, []result{{ctx: "c", out: ""}}))
		assert.Empty(t, b.String())
	})
	t.Run("stream cannot be rendered", func(t *testing.T) {
		assert.Error(t, render(&bytes.Buffer{}, formatStream, false, sampleResults))
	})
}

func Test_runCollect(t *testing.T) {
	t.Run("captures stdout per context in order, stderr streams", func(t *testing.T) {
		fakeKubectl(t, `printf 'NAME\tVAL\n%s\t1\n' "$1"; echo "warn:$1" >&2`)
		var se syncBuf
		results, err := runCollect(context.Background(), []string{"zz", "aa"}, replaceArgs([]string{"x"}, ""), &se)
		require.NoError(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, "zz", results[0].ctx)
		assert.Equal(t, "NAME\tVAL\n--context=zz\t1\n", results[0].out)
		assert.Equal(t, "aa", results[1].ctx)
		assert.Contains(t, se.String(), "warn:--context=aa\n")
	})
	t.Run("partial failure still returns collected output", func(t *testing.T) {
		fakeKubectl(t, `[ "$1" = "--context=bad" ] && exit 5; echo ok`)
		var se syncBuf
		results, err := runCollect(context.Background(), []string{"good", "bad"}, replaceArgs([]string{"x"}, ""), &se)
		require.ErrorContains(t, err, `context "bad"`)
		assert.Equal(t, "ok\n", results[0].out)
		assert.Empty(t, results[1].out)
	})
}
