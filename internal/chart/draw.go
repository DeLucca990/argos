package chart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const (
	width = 600
	scale = 2
	pad   = 28

	titleSize = 26
	subSize   = 18
	labelSize = 18
	tickSize  = 16
)

var (
	surface   = rgb(0xfcfcfb)
	ink       = rgb(0x0b0b0b)
	secondary = rgb(0x52514e)
	muted     = rgb(0x898781)
	gridline  = rgb(0xe1e0d9)
	baseline  = rgb(0xc3c2b7)
	accent    = rgb(0x2a78d6)
	light     = rgb(0x86b6ef)
)

func rgb(v uint32) color.RGBA {
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

type typeface struct{ regular, bold *opentype.Font }

var typefaces = sync.OnceValues(func() (typeface, error) {
	regular, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return typeface{}, err
	}
	bold, err := opentype.Parse(gobold.TTF)
	return typeface{regular, bold}, err
})

type faceKey struct {
	size float64
	bold bool
}

type canvas struct {
	img   *image.RGBA
	tf    typeface
	faces map[faceKey]font.Face
	buf   sfnt.Buffer
}

func newCanvas(height float64) (*canvas, error) {
	tf, err := typefaces()
	if err != nil {
		return nil, fmt.Errorf("the fonts: %w", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width*scale, int(math.Ceil(height))*scale))
	draw.Draw(img, img.Bounds(), image.NewUniform(surface), image.Point{}, draw.Src)
	return &canvas{img: img, tf: tf, faces: map[faceKey]font.Face{}}, nil
}

func (c *canvas) face(size float64, bold bool) font.Face {
	k := faceKey{size, bold}
	if f, ok := c.faces[k]; ok {
		return f
	}
	src := c.tf.regular
	if bold {
		src = c.tf.bold
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{Size: size * scale, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	c.faces[k] = f
	return f
}

func (c *canvas) clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		if i, err := c.tf.regular.GlyphIndex(&c.buf, r); err != nil || i == 0 {
			return -1
		}
		return r
	}, s)
}

func (c *canvas) measure(s string, size float64, bold bool) float64 {
	return float64(font.MeasureString(c.face(size, bold), c.clean(s))) / 64 / scale
}

func (c *canvas) fit(s string, size float64, bold bool, max float64) string {
	s = strings.TrimSpace(c.clean(s))
	if c.measure(s, size, bold) <= max {
		return s
	}
	r := []rune(s)
	for len(r) > 1 && c.measure(string(r)+"…", size, bold) > max {
		r = r[:len(r)-1]
	}
	return strings.TrimSpace(string(r)) + "…"
}

func (c *canvas) text(x, y float64, s string, size float64, bold bool, col color.Color) {
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: c.face(size, bold),
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * scale * 64), Y: fixed.Int26_6(y * scale * 64)}}
	d.DrawString(c.clean(s))
}

func (c *canvas) textRight(x, y float64, s string, size float64, bold bool, col color.Color) {
	c.text(x-c.measure(s, size, bold), y, s, size, bold, col)
}

func (c *canvas) textCenter(x, y float64, s string, size float64, bold bool, col color.Color) {
	c.text(x-c.measure(s, size, bold)/2, y, s, size, bold, col)
}

type pt struct{ x, y float64 }

func (c *canvas) fill(col color.Color, shapes ...[]pt) {
	b := c.img.Bounds()
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	r.DrawOp = draw.Over
	for _, s := range shapes {
		if len(s) < 3 {
			continue
		}
		if area(s) < 0 {
			s = reversed(s)
		}
		r.MoveTo(float32(s[0].x*scale), float32(s[0].y*scale))
		for _, p := range s[1:] {
			r.LineTo(float32(p.x*scale), float32(p.y*scale))
		}
		r.ClosePath()
	}
	r.Draw(c.img, b, image.NewUniform(col), image.Point{})
}

func area(s []pt) float64 {
	var a float64
	for i, p := range s {
		q := s[(i+1)%len(s)]
		a += p.x*q.y - q.x*p.y
	}
	return a
}

func reversed(s []pt) []pt {
	out := make([]pt, len(s))
	for i, p := range s {
		out[len(s)-1-i] = p
	}
	return out
}

func (c *canvas) rect(x0, y0, x1, y1 float64, col color.Color) {
	c.fill(col, []pt{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}})
}

func (c *canvas) rounded(x0, y0, x1, y1 float64, tl, tr, br, bl float64, col color.Color) {
	var s []pt
	corner := func(cx, cy, r, from float64) {
		if r <= 0 {
			s = append(s, pt{cx, cy})
			return
		}
		for i := 0; i <= 8; i++ {
			a := from + float64(i)*math.Pi/16
			s = append(s, pt{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		}
	}
	corner(x0+tl, y0+tl, tl, math.Pi)
	corner(x1-tr, y0+tr, tr, -math.Pi/2)
	corner(x1-br, y1-br, br, 0)
	corner(x0+bl, y1-bl, bl, math.Pi/2)
	c.fill(col, s)
}

func circle(x, y, r float64) []pt {
	s := make([]pt, 0, 24)
	for i := range 24 {
		a := float64(i) * 2 * math.Pi / 24
		s = append(s, pt{x + r*math.Cos(a), y + r*math.Sin(a)})
	}
	return s
}

func (c *canvas) dot(x, y, r float64, col color.Color) { c.fill(col, circle(x, y, r)) }

func (c *canvas) line(points []pt, w float64, col color.Color) {
	var shapes [][]pt
	for i, p := range points {
		shapes = append(shapes, circle(p.x, p.y, w/2))
		if i == 0 {
			continue
		}
		q := points[i-1]
		dx, dy := p.x-q.x, p.y-q.y
		n := math.Hypot(dx, dy)
		if n == 0 {
			continue
		}
		nx, ny := -dy/n*w/2, dx/n*w/2
		shapes = append(shapes, []pt{{q.x + nx, q.y + ny}, {p.x + nx, p.y + ny}, {p.x - nx, p.y - ny}, {q.x - nx, q.y - ny}})
	}
	c.fill(col, shapes...)
}

func (c *canvas) hline(x0, x1, y, w float64, col color.Color) { c.rect(x0, y-w/2, x1, y+w/2, col) }

func (c *canvas) vline(x, y0, y1, w float64, col color.Color) { c.rect(x-w/2, y0, x+w/2, y1, col) }

func (c *canvas) header(title, subtitle string) float64 {
	y := pad + titleSize*0.8
	size := float64(titleSize)
	for size > 18 && c.measure(title, size, true) > width-2*pad {
		size -= 2
	}
	c.text(pad, y, c.fit(title, size, true, width-2*pad), size, true, ink)
	y += subSize + 12
	c.text(pad, y, c.fit(subtitle, subSize, false, width-2*pad), subSize, false, secondary)
	return y
}

func (c *canvas) png() ([]byte, error) {
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, c.img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func ticks(top int64) (step, max int64) {
	if top <= 0 {
		top = 100
	}
	raw := float64(top) / 4
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	s := 10 * mag
	for _, m := range []float64{1, 2, 2.5, 5} {
		if m*mag >= raw {
			s = m * mag
			break
		}
	}
	step = int64(math.Ceil(s))
	return step, (top + step - 1) / step * step
}

func thousands(n int64) string {
	digits := fmt.Sprint(n)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(d)
	}
	return b.String()
}

func tickLabel(cents int64) string {
	if cents%100 == 0 {
		return "R$ " + thousands(cents/100)
	}
	return fmt.Sprintf("R$ %s,%02d", thousands(cents/100), cents%100)
}

func amount(cents int64) string {
	if cents >= 10000 {
		return "R$ " + thousands((cents+50)/100)
	}
	return tickLabel(cents)
}
