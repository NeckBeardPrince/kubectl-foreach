# kubectl foreach

Run a `kubectl` command in one or more contexts (clusters) in parallel (similar
to GNU parallel/xargs). Useful for querying multiple clusters at once or making
changes against the cluster fleet.

## Usage

```text
Usage:
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
               is a header (kept once)
    -q         Disable and accept confirmation prompts ($KUBECTL_FOREACH_DISABLE_PROMPTS) 
    -h/--help  Print help
```

## Demo

Query a pod by label in `minikube` and `*-prod*` contexts:

```text
$ kubectl foreach /-prod/ minikube -- get pods -n kube-system --selector compute.twitter.com/app=coredns --no-headers

     eu-prod | coredns-59bd9867bb-6rbx7   2/2     Running   0          78d
     eu-prod | coredns-59bd9867bb-9xczh   2/2     Running   0          78d
     eu-prod | coredns-59bd9867bb-fvn6t   2/2     Running   0          78d
    minikube | No resources found in kube-system namespace.
 useast-prod | coredns-6fd4bd9db4-7w9wv   2/2     Running   0          78d
 useast-prod | coredns-6fd4bd9db4-9pk8n   2/2     Running   0          78d
 useast-prod | coredns-6fd4bd9db4-xphr4   2/2     Running   0          78d
 uswest-prod | coredns-6f987df9bc-6fgc2   2/2     Running   0          78d
 uswest-prod | coredns-6f987df9bc-9gxvt   2/2     Running   0          78d
 uswest-prod | coredns-6f987df9bc-d88jk   2/2     Running   0          78d
```

## Examples

**Match to contexts by name:** Run a command ("kubectl version") on contexts `c1`, `c2`
and `c3`:

```sh
kubectl foreach c1 c2 c3 -- version
```

**Match to contexts by pattern:** Run a command on contexts starting with `gke`
(regular expression syntax):

```sh
kubectl foreach /^gke/ -- get pods
```

**Match all contexts:** empty context matches all contexts.

```sh
kubectl foreach -- version
```

**Excluding contexts:** Use the matching syntaxes with a `^` prefix to use them
for exclusion. If no matching contexts are specified.

e.g. match all contexts **except** `c1` and except those ending
with `prod` (single quotes for escaping `$` in the shell):

```shell
kubectl foreach ^c1 ^/prod'$'/ -- version
```

**Using with kubectl plugins:** Customize how context name is passed to the command
(useful for kubectl plugins as `--context` must be specified after plugin name).

In this example, `_` is replaced with the context name when calling "kubectl
my_plugin".

```shell
kubectl foreach -I _ -- my_plugin -ctx=_
```

**Limit parallelization:** Only run 3 commands at a time:

```
kubectl foreach -c 3 /^gke-/
```

**Structured output:** Collect results from every context into one table, CSV,
or Markdown document. Output is split into columns on tabs (or on runs of 2+
spaces, like `kubectl get`), and a `CONTEXT` column is added. Use `-H` when the
command prints a header line (it is kept once instead of once per context).
Prefer `custom-columns` or `jsonpath` with tabs for clean columns:

```
kubectl foreach -q -o csv -H /prod/ -- get pods -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name > pods.csv

kubectl foreach -q -o markdown -H -- get ingress -A \
  -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,HOSTS:.spec.rules[*].host'
```

Colors are stripped automatically when output is piped or redirected.
`stderr` (warnings, errors) still streams live, prefixed with the context.

## Install

Use [Krew](https://krew.sigs.k8s.io/) kubectl plugin manager:

```shell
kubectl krew install foreach
```

You can also build from source but you won't receive new version updates:
```
go install github.com/NeckBeardPrince/kubectl-foreach@latest
```

## Remarks/FAQ

**Do not use this tool programmatically:**

This tool is not intended for deploying workloads to clusters, or using
programmatically. Therefore, it does not provide a structured output format or
ordered printing that is meant to be parsed by or piped to other programs (maybe
except for `grep`).

**error: pipe: too many open files:**

macOS default open files limit seems to be 256. kubectl command opens files
and sockets that easily exhausts this number while running the command against
50+ clusters. Run `ulimit -n 2048` to bump this limit to a higher number and
you should not be seeing the error anymore.

