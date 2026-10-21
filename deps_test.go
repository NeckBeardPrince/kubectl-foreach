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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKubectl installs a `kubectl` shell script at the front of PATH.
func fakeKubectl(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl uses a POSIX shell script")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// syncBuf is a goroutine-safe buffer for capturing output of parallel runs.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lipgloss + colorprofile: guards the color table against upstream behavior changes.
func Test_colors_lipgloss(t *testing.T) {
	require.NotEmpty(t, colors)
	for i, c := range colors {
		assert.Contains(t, c(fmt.Sprintf("ctx-%d", i)), "ctx-", "color %d must keep the text", i)
	}
	// a context name with a percent sign must survive untouched (not a format string)
	assert.Contains(t, colors[0]("a%sb"), "a%sb")

	styled := style(red8, noColor)("x")
	require.Contains(t, styled, "\x1b[", "lipgloss Render always emits ANSI")

	var plain, color bytes.Buffer
	_, err := (&colorprofile.Writer{Forward: &plain, Profile: colorprofile.NoTTY}).Write([]byte(styled))
	require.NoError(t, err)
	assert.Equal(t, "x", plain.String(), "non-TTY output must have ANSI stripped")

	_, err = (&colorprofile.Writer{Forward: &color, Profile: colorprofile.ANSI}).Write([]byte(styled))
	require.NoError(t, err)
	assert.Contains(t, color.String(), "\x1b[")
}

func Test_kubeContexts(t *testing.T) {
	t.Run("lists contexts", func(t *testing.T) {
		fakeKubectl(t, `printf 'a\nb\n\n'`)
		got, err := kubeContexts(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, got)
	})
	t.Run("empty kubeconfig yields no contexts", func(t *testing.T) {
		fakeKubectl(t, `true`)
		got, err := kubeContexts(context.Background())
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("kubectl failure", func(t *testing.T) {
		fakeKubectl(t, `exit 3`)
		_, err := kubeContexts(context.Background())
		assert.ErrorContains(t, err, "failed to get contexts")
	})
}

func Test_parseContextNames(t *testing.T) {
	assert.Empty(t, parseContextNames(""))
	assert.Empty(t, parseContextNames("\n \n"))
	assert.Equal(t, []string{"a", "b"}, parseContextNames(" a \r\nb\n"))
}

// errgroup: SetLimit, error propagation, and output prefixing end to end.
func Test_runAll_errgroup(t *testing.T) {
	t.Run("prefixes output per context", func(t *testing.T) {
		fakeKubectl(t, `echo "out:$1"; echo "err:$1" >&2`)
		var so, se syncBuf
		prev := *workers
		*workers = 2
		t.Cleanup(func() { *workers = prev })

		err := runAll(context.Background(), []string{"c1", "c22"}, replaceArgs([]string{"x"}, ""), &so, &se)
		require.NoError(t, err)
		assert.Contains(t, so.String(), "out:--context=c1\n")
		assert.Contains(t, se.String(), "err:--context=c22\n")
		assert.Equal(t, 2, strings.Count(so.String(), " | "))
	})

	t.Run("failure names the context", func(t *testing.T) {
		fakeKubectl(t, `[ "$1" = "--context=bad" ] && exit 7; exit 0`)
		var so, se syncBuf
		err := runAll(context.Background(), []string{"good", "bad"}, replaceArgs([]string{"x"}, ""), &so, &se)
		require.Error(t, err)
		assert.ErrorContains(t, err, `context "bad"`)
		assert.ErrorContains(t, err, "exit status 7")
	})

	t.Run("arg replacement error is returned", func(t *testing.T) {
		fakeKubectl(t, `true`)
		var so, se syncBuf
		err := runAll(context.Background(), []string{"a"}, replaceArgs([]string{"x"}, "NOPE"), &so, &se)
		assert.ErrorContains(t, err, "did not use context name replacement")
	})

	t.Run("honors -c limit", func(t *testing.T) {
		// each run holds a lock dir; a second concurrent run would fail to mkdir it
		lock := filepath.Join(t.TempDir(), "lock")
		fakeKubectl(t, `mkdir "`+lock+`" || exit 9; sleep 0.1; rmdir "`+lock+`"`)
		var so, se syncBuf
		prev := *workers
		*workers = 1
		t.Cleanup(func() { *workers = prev })

		err := runAll(context.Background(), []string{"a", "b", "c"}, replaceArgs([]string{"x"}, ""), &so, &se)
		assert.NoError(t, err)
	})
}

func Test_prompt_closedStdin(t *testing.T) {
	// EOF without input must not hang and must not count as acceptance
	assert.EqualError(t, prompt(context.Background(), strings.NewReader("")), "user refused execution")
	assert.Error(t, prompt(context.Background(), iotest.ErrReader(os.ErrClosed)))
}

func Test_findKubeContextArg_lookalikes(t *testing.T) {
	_, ok := findKubeContextArg([]string{"--contextual"})
	assert.False(t, ok)
}

func Test_prefixingWriter_doubleClose(t *testing.T) {
	var b bytes.Buffer
	pw := &prefixingWriter{prefix: []byte("p "), w: &b}
	_, _ = pw.Write([]byte("tail"))
	require.NoError(t, pw.Close())
	require.NoError(t, pw.Close())
	assert.Equal(t, "p tail\n", b.String())
}
