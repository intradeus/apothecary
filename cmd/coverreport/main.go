// coverreport turns a merged Go coverage profile into a page: every package,
// its statements and how many ran, worst first, with a link into `go tool
// cover`'s per-file view. It fails when the total falls under the floor, which
// is how `task verify` holds it.
//
//	coverreport -profile coverage/go/profile.out -out coverage/go/index.html -min 90
package main

import (
	"bufio"
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Row is one package's line in the report.
type Row struct {
	Package string
	Total   int
	Covered int
}

// Percent is the share of the package's statements that ran.
func (r Row) Percent() float64 {
	if r.Total == 0 {
		return 100
	}
	return 100 * float64(r.Covered) / float64(r.Total)
}

// Missing is how many statements never ran.
func (r Row) Missing() int { return r.Total - r.Covered }

var blockLine = regexp.MustCompile(`^(.+):(\d+\.\d+,\d+\.\d+) (\d+) (\d+)$`)

// Parse reads a profile into per-package rows, most missing statements first.
// With -coverpkg a block appears once per test binary: it counts once, covered
// if any binary ran it.
func Parse(r io.Reader) ([]Row, error) {
	type block struct{ file, span string }
	stmts := map[block]int{}
	ran := map[block]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		m := blockLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("not a coverage line: %q", line)
		}
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return nil, fmt.Errorf("statements in %q: %w", line, err)
		}
		count, err := strconv.Atoi(m[4])
		if err != nil {
			return nil, fmt.Errorf("count in %q: %w", line, err)
		}
		b := block{m[1], m[2]}
		stmts[b] = n
		ran[b] = ran[b] || count > 0
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	byPkg := map[string]*Row{}
	for b, n := range stmts {
		name := path.Dir(b.file)
		if testOnly(name) {
			continue
		}
		row := byPkg[name]
		if row == nil {
			row = &Row{Package: name}
			byPkg[name] = row
		}
		row.Total += n
		if ran[b] {
			row.Covered += n
		}
	}
	rows := make([]Row, 0, len(byPkg))
	for _, row := range byPkg {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if a, b := rows[i].Missing(), rows[j].Missing(); a != b {
			return a > b
		}
		return rows[i].Package < rows[j].Package
	})
	return rows, nil
}

// testOnly reports a package that exists to serve tests: pgtest, skilltest and
// the other contract suites, by the name they share. What never runs in one is
// its own failure branches, which fire only when the code under test is
// broken, so counting them would measure nothing.
func testOnly(pkg string) bool { return strings.HasSuffix(path.Base(pkg), "test") }

// Total sums the rows.
func Total(rows []Row) Row {
	sum := Row{Package: "total"}
	for _, r := range rows {
		sum.Total += r.Total
		sum.Covered += r.Covered
	}
	return sum
}

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"pct": func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) },
}).Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Backend coverage</title>
<style>
	:root { color-scheme: light dark; --ok: #3c7a3a; --warn: #9a7410; --bad: #c2334a; }
	body { font: 14px/1.5 system-ui, sans-serif; margin: 2rem auto; max-width: 980px; padding: 0 1rem; }
	h1 { font-size: 22px; margin: 0; }
	.sum { margin: 4px 0 20px; opacity: .75; }
	table { border-collapse: collapse; width: 100%; font-variant-numeric: tabular-nums; }
	th { text-align: right; font-weight: 500; opacity: .7; padding: 4px 10px; }
	th:first-child, td:first-child { text-align: left; }
	td { text-align: right; padding: 3px 10px; border-top: 1px solid #8884; white-space: nowrap; }
	td:first-child { font-family: ui-monospace, monospace; font-size: 13px; }
	.bar { width: 180px; }
	.bar i { display: block; height: 8px; border-radius: 4px; background: var(--c); }
	.good { --c: var(--ok); } .fair { --c: var(--warn); } .poor { --c: var(--bad); }
</style>
<h1>Backend coverage: {{pct .Total.Percent}}%</h1>
<p class="sum">{{.Total.Covered}} of {{.Total.Total}} statements. Floor {{pct .Min}}%. Worst first.
Test-only packages (<code>*test</code>) aren't counted. <a href="files.html">Line by line</a>.</p>
<table>
<tr><th>Package<th>Coverage<th><th>Missing<th>Statements
{{range .Rows}}<tr class="{{if ge .Percent 90.0}}good{{else if ge .Percent 70.0}}fair{{else}}poor{{end}}">
<td>{{.Package}}<td>{{pct .Percent}}%<td class="bar"><i style="width:{{pct .Percent}}%"></i><td>{{.Missing}}<td>{{.Total}}
{{end}}</table>
</html>
`))

// Render writes the report.
func Render(w io.Writer, rows []Row, minimum float64) error {
	return page.Execute(w, struct {
		Rows  []Row
		Total Row
		Min   float64
	}{rows, Total(rows), minimum})
}

func run(profile, out string, minimum float64, stdout io.Writer) error {
	f, err := os.Open(profile)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := Parse(f)
	if err != nil {
		return err
	}
	if out != "" {
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		defer w.Close()
		if err := Render(w, rows, minimum); err != nil {
			return err
		}
	}
	total := Total(rows)
	fmt.Fprintf(stdout, "backend coverage: %.1f%% (%d of %d statements)\n", total.Percent(), total.Covered, total.Total)
	if total.Percent() < minimum {
		return fmt.Errorf("under the floor of %.1f%%", minimum)
	}
	return nil
}

func main() {
	profile := flag.String("profile", "", "merged coverage profile")
	out := flag.String("out", "", "HTML report to write")
	minimum := flag.Float64("min", 0, "fail under this total percentage")
	flag.Parse()
	if err := run(*profile, *out, *minimum, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "coverreport:", err)
		os.Exit(1)
	}
}
