package task

import (
	"fmt"
	"strings"
)

// Mermaid returns the run's state diagram, generated from the step table. testdata/steps.mmd
// is its golden copy and SKETCH.md embeds it, and a test fails the build when either differs,
// so the picture can't drift from the steps. Guards are tested apart: a matching diagram only
// proves the shape.
//
// Solid edges are the main line. Dotted edges are skips, failures and early ends.
func Mermaid() string {
	var b strings.Builder
	edge := func(from, to, label string, dotted bool) {
		arrow := "-->"
		if dotted {
			arrow = "-.->"
		}
		if label != "" {
			arrow += "|" + label + "|"
		}
		fmt.Fprintf(&b, "    %s %s %s\n", from, arrow, to)
	}
	b.WriteString("flowchart TD\n")
	b.WriteString("    submitted([\"submitted\"])\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "    %s[\"%s: %s\"]\n", s.step, s.step, s.doc)
	}
	b.WriteString("    ended([\"ended\"])\n")

	var main, wrap []stepInfo
	for _, s := range steps {
		if s.wrapUp {
			wrap = append(wrap, s)
		} else {
			main = append(main, s)
		}
	}
	firstWrap := wrap[0].step
	collect := wrap[0].step
	for _, s := range wrap {
		if !s.early {
			collect = s.step
			break
		}
	}

	edge("submitted", string(main[0].step), "", false)
	edge("submitted", string(firstWrap), "ends while queued", true)
	edge(string(StepMachine), string(StepMachine), "refused, queued again", true)
	for i, s := range main {
		next := collect
		if i+1 < len(main) {
			next = main[i+1].step
		}
		edge(string(s.step), string(next), "", false)
		if i+2 <= len(main) && next != collect {
			n := main[i+1]
			if n.skip != nil {
				after := collect
				if i+2 < len(main) {
					after = main[i+2].step
				}
				edge(string(s.step), string(after), n.skipWhy, true)
			}
		}
		if s.stage != "" && next != collect {
			edge(string(s.step), string(collect), "fails", true)
		}
		edge(string(s.step), string(firstWrap), "ends early", true)
	}
	for i, s := range wrap {
		if i+1 < len(wrap) {
			edge(string(s.step), string(wrap[i+1].step), "", false)
		}
	}
	edge(string(wrap[len(wrap)-1].step), "ended", "", false)
	return b.String()
}
