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
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charmbracelet/colorprofile"
	"golang.org/x/sync/errgroup"
)

const (
	envDisablePrompts = `KUBECTL_FOREACH_DISABLE_PROMPTS`
)

var (
	gray = style(gray8, noColor)
	red  = style(red8, noColor)

	// stderrW is wrapped by main so colors are stripped when stderr is not a TTY
	stderrW io.Writer = os.Stderr

	fl      = flag.NewFlagSet("kubectl foreach", flag.ContinueOnError)
	repl    = fl.String("I", "", "string to replace in cmd args with context name (like xargs -I)")
	workers = fl.Int("c", 0, "parallel runs (default: as many as matched contexts)")
	quiet   = fl.Bool("q", false, "accept confirmation prompts")
	outFmt  = fl.String("o", string(formatStream), "output format: stream, table, csv, markdown")
	header  = fl.Bool("H", false, "treat the first output line of each context as a header (table, csv, markdown)")
)

func printErrAndExit(msg string) {
	fmt.Fprintf(stderrW, "%s%s\n", red("error: "), msg)
	os.Exit(1)
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
    kubectl foreach [OPTIONS] [PATTERN]... -- [KUBECTL_ARGS...]

Patterns can be used to match context names from kubeconfig:
      (empty): matches all contexts
         NAME: matches context with exact name
    /PATTERN/: matches context with regular expression
        ^NAME: remove context with exact name from the matched results
   ^/PATTERN/: remove contexts matching the regular expression from the results

Options:
    -c=NUM     Limit parallel executions (default: 0, unlimited)
    -I=VAL     Replace VAL occurring in KUBECTL_ARGS with context name
    -o=FORMAT  Output format: stream (default), table, csv, markdown
    -H         With -o table/csv/markdown: first output line of each context
               is a header (kept once). Use kubectl's custom-columns or
               jsonpath for clean columns.
    -q         Disable and accept confirmation prompts ($KUBECTL_FOREACH_DISABLE_PROMPTS)
    -h/--help  Print help

Examples:
    # get nodes on contexts named a b c
    kubectl foreach a b c -- get nodes

    # get nodes on all contexts named c0..9 except c1 (note the escaping)
    kubectl foreach '/^c[0-9]/' ^c1	 -- get nodes

    # get nodes on all contexts that has "prod" but not "foo"
    kubectl foreach /prod/ ^/foo/ -- get nodes

    # use 'kubectl tail' plugin to follow logs of pods in contexts named *test*
    kubectl foreach -I _ /test/ -- tail --context=_ -l app=foo

    # one CSV of all pods across prod contexts (header kept once)
    kubectl foreach -q -o csv -H /prod/ -- get pods -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name`+"\n")
	os.Exit(0)
}

func main() {
	stderrW = colorprofile.NewWriter(os.Stderr, os.Environ())
	log.SetOutput(stderrW)
	log.SetFlags(0)
	fl.Usage = func() { printUsage(os.Stderr) }

	if err := fl.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(os.Stderr)
		}
		printErrAndExit(err.Error())
	}
	_, kubectlArgs, err := separateArgs(os.Args[1:])
	if err != nil {
		printErrAndExit(fmt.Errorf("failed to parse command-line arguments: %w. see -h/--help", err).Error())
	}

	ctx := context.Background()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// initialize signal handler after
	go func() {
		<-ctx.Done()
		fmt.Fprintln(stderrW, gray("received exit signal"))
	}()

	if *workers < 0 {
		printErrAndExit("-c < 0")
	}
	format, err := parseFormat(*outFmt)
	if err != nil {
		printErrAndExit(err.Error())
	}
	if *header && !format.buffered() {
		printErrAndExit("-H requires -o table, csv, or markdown")
	}

	ctxs, err := kubeContexts(ctx)
	if err != nil {
		printErrAndExit(err.Error())
	}
	var filters []filter

	// re-parse flags to extract positional arguments of the tool, minus '--' + kubectl args
	if err := fl.Parse(trimSuffix(os.Args[1:], append([]string{"--"}, kubectlArgs...))); err != nil {
		printErrAndExit(err.Error())
	}
	for _, arg := range fl.Args() {
		f, err := parseFilter(arg)
		if err != nil {
			printErrAndExit(err.Error())
		}
		filters = append(filters, f)
	}

	// heuristic: if --context is already in kubectl args (and no -I flag), user is likely making a mistake
	if ctxArg, found := findKubeContextArg(kubectlArgs); found && *repl == "" {
		printErrAndExit(fmt.Sprintf(
			"kubectl args already contain %q flag, this is likely a mistake (otherwise the program would not query different contexts)", ctxArg))
	}

	ctxMatches := matchContexts(ctxs, filters)

	if len(ctxMatches) == 0 {
		printErrAndExit("query matched no contexts from kubeconfig")
	}

	fmt.Fprintln(stderrW, "Will run command in context(s):")
	for _, c := range ctxMatches {
		fmt.Fprint(stderrW, gray(fmt.Sprintf("  - %s\n", c)))
	}
	if !*quiet && os.Getenv(envDisablePrompts) == "" {
		fmt.Fprintf(stderrW, "Continue? [Y/n]: ")
		if err := prompt(ctx, os.Stdin); err != nil {
			printErrAndExit(err.Error())
		}
	}

	stdoutW := colorprofile.NewWriter(os.Stdout, os.Environ())
	syncOut := &synchronizedWriter{Writer: stdoutW}
	syncErr := &synchronizedWriter{Writer: stderrW}
	argMaker := replaceArgs(kubectlArgs, *repl)

	if !format.buffered() {
		if err := runAll(ctx, ctxMatches, argMaker, syncOut, syncErr); err != nil {
			printErrAndExit(err.Error())
		}
		return
	}

	results, runErr := runCollect(ctx, ctxMatches, argMaker, syncErr)
	// render what we have even if some contexts failed, then report the failure
	if err := render(stdoutW, format, *header, results); err != nil {
		printErrAndExit(err.Error())
	}
	if runErr != nil {
		printErrAndExit(runErr.Error())
	}
}

func replaceArgs(args []string, repl string) func(ctx string) ([]string, error) {
	return func(ctx string) ([]string, error) {
		if repl == "" {
			return append([]string{"--context=" + ctx}, args...), nil
		}
		out := make([]string, len(args))
		modified := false
		for i := range args {
			out[i] = strings.ReplaceAll(args[i], repl, ctx)
			modified = modified || (out[i] != args[i])
		}
		if !modified {
			return nil, fmt.Errorf("args did not use context name replacement string %q", repl)
		}
		return out, nil
	}
}

func kubeContexts(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "config", "get-contexts", "-o=name")
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = os.Stderr // TODO might be redundant
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to get contexts: %w", err)
	}
	return parseContextNames(b.String()), nil
}

// parseContextNames splits `kubectl config get-contexts -o=name` output into
// context names, ignoring blank lines (an empty kubeconfig yields no names).
func parseContextNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func runAll(ctx context.Context, kubeCtxs []string, argMaker func(string) ([]string, error), stdout, stderr io.Writer) error {
	return runEach(ctx, kubeCtxs, argMaker, stderr, func(_ string, prefix []byte) io.WriteCloser {
		return &prefixingWriter{prefix: prefix, w: stdout}
	})
}

// runCollect runs kubectl in every context, capturing each context's stdout
// (stderr still streams, prefixed). Results follow kubeCtxs order.
func runCollect(ctx context.Context, kubeCtxs []string, argMaker func(string) ([]string, error), stderr io.Writer) ([]result, error) {
	bufs := make(map[string]*bufCloser, len(kubeCtxs))
	for _, c := range kubeCtxs {
		bufs[c] = &bufCloser{}
	}
	err := runEach(ctx, kubeCtxs, argMaker, stderr, func(kctx string, _ []byte) io.WriteCloser {
		return bufs[kctx]
	})
	results := make([]result, len(kubeCtxs))
	for i, c := range kubeCtxs {
		results[i] = result{ctx: c, out: bufs[c].String()}
	}
	return results, err
}

// runEach runs kubectl once per context in parallel. outFor supplies each
// context's stdout writer; stderr is always prefixed with the context name.
func runEach(ctx context.Context, kubeCtxs []string, argMaker func(string) ([]string, error), stderr io.Writer, outFor func(kctx string, prefix []byte) io.WriteCloser) error {
	n := len(kubeCtxs)
	if *workers > 0 {
		n = *workers
	}

	wg, _ := errgroup.WithContext(ctx)
	wg.SetLimit(n)

	maxLen := maxLen(kubeCtxs)
	leftPad := func(s string, origLen int) string {
		return strings.Repeat(" ", maxLen-origLen) + s
	}

	for i, kctx := range kubeCtxs {
		colFn := colors[i%len(colors)]
		wg.Go(func() error {
			prefix := []byte(leftPad(colFn(kctx), len(kctx)) + " | ")
			wo := outFor(kctx, prefix)
			we := &prefixingWriter{prefix: prefix, w: stderr}
			args, err := argMaker(kctx)
			if err != nil {
				return err
			}
			if err := run(ctx, args, wo, we); err != nil {
				return fmt.Errorf("context %q: %w", kctx, err)
			}
			return nil
		})
	}
	return wg.Wait()
}

func maxLen(s []string) int {
	max := 0
	for _, v := range s {
		if len(v) > max {
			max = len(v)
		}
	}
	return max
}

func run(ctx context.Context, args []string, stdout, stderr io.WriteCloser) (err error) {
	defer func() {
		// flush underlying writer (prefixWriter) by closing in case last output does not terminate with newline
		if err := stdout.Close(); err != nil {
			log.Printf("WARN: failed to close stdout: %v", err)
		}
		if err := stderr.Close(); err != nil {
			log.Printf("WARN: failed to close stderr: %v", err)
		}
	}()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// prompt returns an error if user rejects or if ctx cancels.
func prompt(ctx context.Context, r io.Reader) error {
	pr, pw := io.Pipe()
	go func() {
		// always close so the scanner sees EOF (e.g. stdin closed) instead of blocking forever
		_, err := io.Copy(pw, r)
		pw.CloseWithError(err)
	}()
	defer pw.Close()

	scanDone := make(chan error, 1)

	go func() {
		s := bufio.NewScanner(pr)
		for s.Scan() {
			if err := s.Err(); err != nil {
				scanDone <- err
				return
			}
			v := s.Text()
			if v == "y" || v == "Y" || v == "" {
				scanDone <- nil
				return
			}
			break
		}
		scanDone <- errors.New("user refused execution")
	}()

	select {
	case res := <-scanDone:
		return res
	case <-ctx.Done():
		pr.Close()
		return fmt.Errorf("prompt canceled")
	}
}

func trimSuffix(a []string, suffix []string) []string {
	if len(suffix) > len(a) {
		return a
	}
	for i, j := len(a)-1, len(suffix)-1; j >= 0; i, j = i-1, j-1 {
		if a[i] != suffix[j] {
			return a
		}
	}
	return a[:len(a)-len(suffix)]
}

func findKubeContextArg(argv []string) (string, bool) {
	for _, arg := range argv {
		if arg == "--context" || strings.HasPrefix(arg, "--context=") {
			return arg, true
		}
	}
	return "", false
}
