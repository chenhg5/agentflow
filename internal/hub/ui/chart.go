package ui

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/chenhg5/agentflow/internal/hub/app"
)

// runsChart draws the daily runs as stacked bars (succeeded, failed, other),
// the same chart the Loops overview shows, as inline SVG with no script.
func runsChart(days []app.Day) template.HTML {
	const width, height, left, right, top, bottom = 720.0, 200.0, 34.0, 6.0, 10.0, 24.0
	if len(days) == 0 {
		return ""
	}
	peak := 0
	for _, day := range days {
		peak = max(peak, day.Counts.Total)
	}
	ceiling := niceCeiling(peak)
	plotW, plotH := width-left-right, height-top-bottom
	slot := plotW / float64(len(days))
	barW := slot * 0.62
	y := func(value int) float64 { return top + plotH - plotH*float64(value)/float64(ceiling) }

	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg class="chart" viewBox="0 0 %.0f %.0f" role="img" aria-label="每天的运行">`, width, height)
	for step := 0; step <= 4; step++ {
		value := ceiling * step / 4
		lineY := y(value)
		fmt.Fprintf(&svg, `<line class="grid" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, left, width-right, lineY, lineY)
		fmt.Fprintf(&svg, `<text class="axis" x="%.1f" y="%.1f" text-anchor="end">%d</text>`, left-8, lineY+4, value)
	}
	labelEvery := (len(days) + 13) / 14
	for index, day := range days {
		x := left + slot*float64(index) + (slot-barW)/2
		counts := day.Counts
		other := counts.Total - counts.Succeeded - counts.Failed
		label := day.Date.Format("01-02")
		fmt.Fprintf(&svg, `<g class="bar"><title>%s：成功 %d · 失败 %d · 其他 %d</title>`, label, counts.Succeeded, counts.Failed, other)
		fmt.Fprintf(&svg, `<rect class="hit" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, left+slot*float64(index), top, slot, plotH)
		base := 0
		for _, part := range []struct {
			class string
			value int
		}{{"good", counts.Succeeded}, {"bad", counts.Failed}, {"other", other}} {
			if part.value <= 0 {
				continue
			}
			y1, y2 := y(base+part.value), y(base)
			fmt.Fprintf(&svg, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, part.class, x, y1, barW, y2-y1)
			base += part.value
		}
		svg.WriteString(`</g>`)
		if index%labelEvery == 0 || index == len(days)-1 {
			fmt.Fprintf(&svg, `<text class="axis" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, x+barW/2, height-6, label)
		}
	}
	svg.WriteString(`</svg>`)
	return template.HTML(svg.String())
}

// niceCeiling rounds up to a value that divides into four readable steps.
func niceCeiling(peak int) int {
	if peak <= 4 {
		return 4
	}
	for _, unit := range []int{1, 2, 3, 4, 5, 10, 15, 20, 25, 50, 75, 100, 150, 200, 250, 500, 1000, 2500, 5000, 10000} {
		if unit*4 >= peak {
			return unit * 4
		}
	}
	return (peak + 3) / 4 * 4
}
