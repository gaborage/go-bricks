// report.go renders captured scenarios: console by default, one self-contained
// static HTML file with -html <path>. Both open with the QUESTION and the
// self-asserting matrix (scenario · step · expected · fired · disposition · ✓/✗).
package main

import (
	"fmt"
	"html"
	"io"
	"strings"
)

// Question is verbatim from the #1308 brief; it heads every report.
const Question = "Do the decided envelope (#1304), seal tags (#1305), family-kid keys (#1306) and replay slots (#1307) compose into a producer/consumer DX that feels right — and do a tampered clear field, a wrong key, a strip-and-re-sign, a rotation overlap, a cross-type reroute, a tenant mismatch and an unsealed body all fail exactly the way those decisions say?"

// MatrixRow is one line of the self-asserting matrix.
type MatrixRow struct {
	Scenario, Step, Expected, Fired, Disposition string
	OK                                           bool
}

func codeLabel(c Code) string {
	if c == codeNone {
		return "(clean)"
	}
	return string(c)
}

// Matrix flattens every step; mismatches counts the ✗ rows.
func Matrix(scenarios []*Scenario) (rows []MatrixRow, mismatches int) {
	for _, s := range scenarios {
		for _, st := range s.Steps {
			r := MatrixRow{Scenario: s.Title, Step: st.Title, Expected: codeLabel(st.Expect), Fired: codeLabel(st.Fired), Disposition: st.Disposition, OK: st.OK()}
			if !r.OK {
				mismatches++
			}
			rows = append(rows, r)
		}
	}
	return rows, mismatches
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// ---------------------------------------------------------------------------
// Console
// ---------------------------------------------------------------------------

func RenderConsole(w io.Writer, scenarios []*Scenario) {
	fmt.Fprintf(w, "QUESTION: %s\n\n", Question)
	RenderMatrixText(w, scenarios)
	fmt.Fprintln(w)
	for _, s := range scenarios {
		fmt.Fprintf(w, "%s\n%s\n%s\n\n", strings.Repeat("=", 100), s.Title, s.Description)
		for _, st := range s.Steps {
			fmt.Fprintf(w, "%s --- %s  [expected %s, fired %s]\n", mark(st.OK()), st.Title, codeLabel(st.Expect), codeLabel(st.Fired))
			if st.Text != "" {
				fmt.Fprintf(w, "    %s\n", st.Text)
			}
			for _, kv := range st.State {
				fmt.Fprintf(w, "    [%s]\n%s\n", kv.Label, indent(kv.Value, "      "))
			}
			if st.Compare != nil {
				fmt.Fprintf(w, "    [Producer wrote]\n%s\n    [Travelled]\n%s\n    [Consumer saw]\n%s\n",
					indent(st.Compare.Wrote, "      "), indent(st.Compare.Travelled, "      "), indent(st.Compare.Saw, "      "))
			}
			fmt.Fprintln(w)
		}
	}
}

// RenderMatrixText prints the matrix as aligned text (also what goes back to the planner).
func RenderMatrixText(w io.Writer, scenarios []*Scenario) {
	rows, mismatches := Matrix(scenarios)
	fmt.Fprintf(w, "MATRIX — %d steps, %d mismatches\n", len(rows), mismatches)
	fmt.Fprintf(w, "%-2s %-34s %-60s %-28s %-28s %s\n", "", "scenario", "step", "expected", "fired", "disposition")
	for _, r := range rows {
		fmt.Fprintf(w, "%-2s %-34s %-60s %-28s %-28s %s\n", mark(r.OK), trunc(r.Scenario, 34), trunc(r.Step, 60), r.Expected, r.Fired, r.Disposition)
	}
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// HTML (CSS-only tabs via radio inputs; one accent colour; inline CSS)
// ---------------------------------------------------------------------------

const htmlHead = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Seal/Open Walkthrough — #1308</title>
<style>
:root{--accent:#2f6f8f;--ink:#1f2328;--muted:#6a737d;--line:#e3e6ea;--bg:#fafbfc;--fail:#b42318;--ok:#1a7f37;--mono:ui-monospace,SFMono-Regular,Menlo,monospace}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif}
header{padding:28px 32px 18px;border-bottom:1px solid var(--line);background:#fff}
header h1{margin:0 0 8px;font-size:20px;font-weight:600}header p{margin:0;max-width:110ch;color:var(--muted)}
header .q{color:var(--ink);font-size:16px;margin-bottom:10px}
table.matrix{border-collapse:collapse;font-size:13px;margin:16px 0 4px;width:100%;max-width:1400px}
table.matrix th,table.matrix td{text-align:left;padding:4px 10px;border-bottom:1px solid var(--line);vertical-align:top}
table.matrix th{color:var(--muted);font-weight:500}
.ok{color:var(--ok);font-weight:600}.bad{color:var(--fail);font-weight:600}
nav{display:flex;flex-wrap:wrap;gap:4px;padding:12px 32px;background:#fff;border-bottom:1px solid var(--line);position:sticky;top:0;z-index:1}
nav label{padding:6px 12px;border:1px solid var(--line);border-radius:6px;cursor:pointer;font-size:13px;color:var(--muted);background:#fff}
input[type=radio]{display:none}
section.tab{display:none;padding:24px 32px 64px;max-width:1400px}
.tab h2{margin:0 0 6px;font-size:18px;font-weight:600}.tab>p{margin:0 0 20px;color:var(--muted);max-width:110ch}
.step{border:1px solid var(--line);border-left:3px solid var(--ok);background:#fff;border-radius:6px;padding:14px 18px;margin-bottom:16px}
.step.bad{border-left-color:var(--fail)}.step h3{margin:0 0 4px;font-size:15px;font-weight:600}
.step .verdict{font-size:12px;color:var(--muted);margin-bottom:6px}
.step .text{margin:0 0 12px;color:var(--muted);font-size:14px;max-width:110ch}
.kv{margin:8px 0}.kv .label{font-size:12px;text-transform:uppercase;letter-spacing:.04em;color:var(--accent);margin-bottom:3px}
.cmp{display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px;margin:10px 0}
pre{margin:0;padding:10px 12px;background:#f4f6f8;border:1px solid var(--line);border-radius:4px;font:12.5px/1.45 var(--mono);white-space:pre-wrap;word-break:break-all;max-height:420px;overflow:auto}
pre.src{max-height:none}
footer{padding:16px 32px;color:var(--muted);font-size:12px;border-top:1px solid var(--line)}
</style></head><body>
`

func RenderHTML(w io.Writer, scenarios []*Scenario) {
	rows, mismatches := Matrix(scenarios)
	fmt.Fprint(w, htmlHead)
	fmt.Fprintf(w, "<header><h1>Seal / Open walkthrough — #1308 prototype</h1><p class=q><strong>Question.</strong> %s</p><p>Static report generated by <code>go run ./research/amqp-seal-prototype -html</code>. Vocabulary per CONTEXT.md “Payload sealing”: Seal, Subject, Two-kid identity, Typed door, Accept-unsealed, Logical kid, Generation, Accept set, Activation, Redelivery, Replay, Duplicate, Dedup key. Card data shown is the published test vector 4111111111111111. Every step declares what it expects; ✓ means fired == expected.</p>\n", html.EscapeString(Question))
	verdict := "ok"
	if mismatches > 0 {
		verdict = "bad"
	}
	fmt.Fprintf(w, "<p><span class=%s>%d steps, %d mismatches</span></p><table class=matrix><tr><th></th><th>scenario</th><th>step</th><th>expected</th><th>fired</th><th>disposition</th></tr>", verdict, len(rows), mismatches)
	for _, r := range rows {
		cls := "ok"
		if !r.OK {
			cls = "bad"
		}
		fmt.Fprintf(w, "<tr><td class=%s>%s</td><td>%s</td><td>%s</td><td><code>%s</code></td><td><code>%s</code></td><td>%s</td></tr>",
			cls, mark(r.OK), html.EscapeString(r.Scenario), html.EscapeString(r.Step), html.EscapeString(r.Expected), html.EscapeString(r.Fired), html.EscapeString(r.Disposition))
	}
	fmt.Fprint(w, "</table></header>\n")

	for i, s := range scenarios {
		checked := ""
		if i == 0 {
			checked = " checked"
		}
		fmt.Fprintf(w, "<input type=radio name=tab id=%s%s>", s.ID, checked)
	}
	fmt.Fprint(w, "<style>")
	for _, s := range scenarios {
		fmt.Fprintf(w, "#%s:checked~nav label[for=%s]{background:var(--accent);color:#fff;border-color:var(--accent)}#%s:checked~section#tab-%s{display:block}", s.ID, s.ID, s.ID, s.ID)
	}
	fmt.Fprint(w, "</style><nav>")
	for _, s := range scenarios {
		fmt.Fprintf(w, "<label for=%s>%s</label>", s.ID, html.EscapeString(s.Title))
	}
	fmt.Fprint(w, "</nav>\n")

	for _, s := range scenarios {
		fmt.Fprintf(w, "<section class=tab id=tab-%s><h2>%s</h2><p>%s</p>\n", s.ID, html.EscapeString(s.Title), html.EscapeString(s.Description))
		for _, st := range s.Steps {
			cls := "step"
			if !st.OK() {
				cls += " bad"
			}
			fmt.Fprintf(w, "<div class=%q id=%q><h3>%s %s</h3><div class=verdict>expected <code>%s</code> · fired <code>%s</code> · %s</div>", cls, st.ID, mark(st.OK()), html.EscapeString(st.Title), html.EscapeString(codeLabel(st.Expect)), html.EscapeString(codeLabel(st.Fired)), html.EscapeString(st.Disposition))
			if st.Text != "" {
				fmt.Fprintf(w, "<p class=text>%s</p>", html.EscapeString(st.Text))
			}
			for _, kv := range st.State {
				pcls := ""
				if strings.HasSuffix(kv.Label, ".go") {
					pcls = " class=src"
				}
				fmt.Fprintf(w, "<div class=kv><div class=label>%s</div><pre%s>%s</pre></div>", html.EscapeString(kv.Label), pcls, html.EscapeString(kv.Value))
			}
			if st.Compare != nil {
				fmt.Fprintf(w, "<div class=cmp><div class=kv><div class=label>Producer wrote</div><pre>%s</pre></div><div class=kv><div class=label>Travelled (exact payload bytes)</div><pre>%s</pre></div><div class=kv><div class=label>Consumer saw</div><pre>%s</pre></div></div>",
					html.EscapeString(st.Compare.Wrote), html.EscapeString(st.Compare.Travelled), html.EscapeString(st.Compare.Saw))
			}
			fmt.Fprint(w, "</div>\n")
		}
		fmt.Fprint(w, "</section>\n")
	}
	fmt.Fprint(w, "<footer>Prototype ≠ implementation: no PayloadError integration, no real keystore module, no messaging client, no ledger table — in-memory stand-ins. Keys are generated per run; jti and the wire bytes differ on every run.</footer></body></html>\n")
}
