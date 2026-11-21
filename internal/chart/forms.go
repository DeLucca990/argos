package chart

import (
	"fmt"
	"image/color"
	"math"
	"strings"
)

const headerEnd = pad + titleSize*0.8 + subSize + 12

type bar struct {
	label string
	cents int64
	other bool
}

type column struct {
	label, sub string
	cents      int64
	partial    bool
	note       string
}

type series struct {
	label  string
	values []int64
}

func (c *canvas) centered(cx, y float64, s string, size float64, bold bool, col color.Color) {
	w := c.measure(s, size, bold)
	x := math.Max(8, math.Min(cx-w/2, width-8-w))
	c.text(x, y, s, size, bold, col)
}

func percent(part, whole int64) int64 { return (part*100 + whole/2) / max(whole, 1) }

func drawBars(title, subtitle string, bars []bar, total int64) ([]byte, error) {
	const rowH, barH = 38.0, 22.0
	top := headerEnd + 24
	c, err := newCanvas(top + float64(len(bars))*rowH - (rowH - barH) + pad)
	if err != nil {
		return nil, err
	}
	c.header(title, subtitle)

	labelW, valueW := 0.0, 0.0
	var most int64
	for _, b := range bars {
		labelW = max(labelW, c.measure(b.label, labelSize, false))
		valueW = max(valueW, c.measure(amount(b.cents)+"  100%", labelSize, false))
		most = max(most, b.cents)
	}
	labelW = min(labelW, width*0.34)
	x0 := pad + labelW + 12
	span := width - pad - valueW - 10 - x0

	for i, b := range bars {
		y := top + float64(i)*rowH
		base := y + barH/2 + labelSize*0.35
		c.textRight(x0-12, base, c.fit(b.label, labelSize, false, labelW), labelSize, false, ink)
		w := max(span*float64(b.cents)/float64(max(most, 1)), 2)
		fill := accent
		if b.other {
			fill = baseline
		}
		r := min(4, w/2)
		c.rounded(x0, y, x0+w, y+barH, 0, r, r, 0, fill)
		value := amount(b.cents)
		c.text(x0+w+8, base, value, labelSize, false, ink)
		c.text(x0+w+8+c.measure(value+" ", labelSize, false), base, fmt.Sprintf("%d%%", percent(b.cents, total)), labelSize, false, muted)
	}
	c.vline(x0, top-6, top+float64(len(bars)-1)*rowH+barH+6, 1, baseline)
	return c.png()
}

func (c *canvas) yAxis(most int64, left, right, plotTop, plotBottom float64) func(int64) float64 {
	step, top := ticks(most)
	y := func(v int64) float64 { return plotBottom - (plotBottom-plotTop)*float64(v)/float64(top) }
	for v := int64(0); v <= top; v += step {
		if v > 0 {
			c.hline(left, right, y(v), 1, gridline)
		}
		c.textRight(left-10, y(v)+tickSize*0.35, tickLabel(v), tickSize, false, muted)
	}
	return y
}

func (c *canvas) tickWidth(most int64) float64 {
	step, top := ticks(most)
	w := 0.0
	for v := int64(0); v <= top; v += step {
		w = max(w, c.measure(tickLabel(v), tickSize, false))
	}
	return w
}

func drawColumns(title, subtitle string, cols []column) ([]byte, error) {
	var notes []string
	for _, col := range cols {
		if col.note != "" {
			notes = append(notes, col.note)
		}
	}
	const plotHeight = 400.0
	height := plotHeight
	if len(notes) > 0 {
		height += 30
	}
	c, err := newCanvas(height)
	if err != nil {
		return nil, err
	}
	c.header(title, subtitle)

	var most int64
	highest := 0
	labelW := 0.0
	for i, col := range cols {
		if col.cents > most {
			most, highest = col.cents, i
		}
		labelW = max(labelW, c.measure(col.label, tickSize, false))
	}
	left, right := pad+c.tickWidth(most)+10, float64(width-pad)
	plotTop, plotBottom := headerEnd+40, plotHeight-pad-2*tickSize-14
	y := c.yAxis(most, left, right, plotTop, plotBottom)

	slot := (right - left) / float64(len(cols))
	colW := min(slot*0.7, 48)
	if slot-colW < 2 {
		colW = max(slot-2, 1)
	}
	every := max(1, int(math.Ceil((labelW+8)/slot)))
	lastSub := ""
	for i, col := range cols {
		cx := left + slot*(float64(i)+0.5)
		top := y(col.cents)
		if col.cents > 0 {
			top = min(top, plotBottom-2)
			fill := accent
			if col.partial {
				fill = light
			}
			r := min(4, colW/2)
			c.rounded(cx-colW/2, top, cx+colW/2, plotBottom, r, r, 0, 0, fill)
		}
		if i%every == 0 {
			c.centered(cx, plotBottom+tickSize+8, col.label, tickSize, false, muted)
			if col.sub != "" && col.sub != lastSub {
				c.centered(cx, plotBottom+2*tickSize+14, col.sub, tickSize, false, muted)
				lastSub = col.sub
			}
		}
		if i == highest && col.cents > 0 {
			c.centered(cx, top-8, amount(col.cents), tickSize, true, ink)
		}
	}
	c.hline(left, right, plotBottom, 1, baseline)

	if len(notes) > 0 {
		fy := plotBottom + 2*tickSize + 44
		c.rounded(pad, fy-12, pad+12, fy, 2, 2, 2, 2, light)
		c.text(pad+20, fy, c.fit("incompleto: "+strings.Join(notes, ", "), tickSize, false, width-2*pad-20), tickSize, false, secondary)
	}
	return c.png()
}

type mark struct {
	index int
	label string
}

func drawPace(title, subtitle string, cur, prev series, days int, marks []mark) ([]byte, error) {
	const height = 420.0
	const legendSize = subSize - 2
	c, err := newCanvas(height)
	if err != nil {
		return nil, err
	}
	c.header(title, subtitle)

	legend := headerEnd + 30
	x := float64(pad)
	for _, s := range []struct {
		series
		fill color.Color
		w    float64
	}{{cur, accent, 3}, {prev, muted, 2.5}} {
		if len(s.values) == 0 {
			continue
		}
		name, value := s.label+"  ", amount(s.values[len(s.values)-1])
		w := 26 + c.measure(name, legendSize, false) + c.measure(value, legendSize, true)
		if x > pad && x+w > width-pad {
			x, legend = pad, legend+26
		}
		c.hline(x, x+18, legend-6, s.w, s.fill)
		c.text(x+26, legend, name, legendSize, false, ink)
		c.text(x+26+c.measure(name, legendSize, false), legend, value, legendSize, true, ink)
		x += w + 28
	}

	var most int64
	for _, v := range append(append([]int64{}, cur.values...), prev.values...) {
		most = max(most, v)
	}
	left, right := pad+c.tickWidth(most)+10, float64(width-pad)
	plotTop, plotBottom := legend+34, height-pad-tickSize-10
	y := c.yAxis(most, left, right, plotTop, plotBottom)
	xAt := func(i int) float64 {
		if days < 2 {
			return (left + right) / 2
		}
		return left + (right-left)*float64(i)/float64(days-1)
	}
	for _, m := range marks {
		c.centered(xAt(m.index), plotBottom+tickSize+10, m.label, tickSize, false, muted)
	}
	c.hline(left, right, plotBottom, 1, baseline)

	path := func(values []int64) []pt {
		points := make([]pt, len(values))
		for i, v := range values {
			points[i] = pt{xAt(i), y(v)}
		}
		return points
	}
	if len(prev.values) > 0 {
		c.line(path(prev.values), 2.5, muted)
	}
	if len(cur.values) > 0 {
		end := path(cur.values)
		c.line(end, 3, accent)
		last := end[len(end)-1]
		c.dot(last.x, last.y, 7, surface)
		c.dot(last.x, last.y, 5, accent)
	}
	return c.png()
}
