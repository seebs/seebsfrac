package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"os"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/faiface/pixel"
	"github.com/faiface/pixel/imdraw"
	"github.com/faiface/pixel/pixelgl"
	"github.com/faiface/pixel/text"
	"github.com/golang/freetype/truetype"

	"golang.org/x/image/font"
)

// flags
const (
	Hide = 1 << iota
	Prune
	FlipX
	FlipY
	FixedC
)

// Mouse activity states. A button starts out Unpressed, then is Pressed,
// may experience Dragging, and is either Unpressed (event cancelled) or
// Released (callback happens).
const (
	Unpressed = iota
	Pressed
	Released
	Dragging
)

var (
	face         font.Face
	atlas        *text.Atlas
	textRenderer *text.Text
	textMatrix   pixel.Matrix
)

// Point represents... actually a line segment, I'm great at this.
type Point struct {
	pixel.Vec
	Flags int
	Color int16
}

func (p Point) String() string {
	return fmt.Sprintf("%.3f, %.3f, 0x%03x, %d", p.X, p.Y, p.Flags, p.Color)
}

// Fractal represents both the underlying data and the current rendered state,
// which in retrospect is a bad decision.
type Fractal struct {
	MaxDepth   int
	Base       []Point
	RenderData `json:"-"` // don't try to log all this junk
}

// RenderData is the rendered/computed data for the fractal.
type RenderData struct {
	MaxOOM        uint
	Total         int
	selectedPoint int
	Inverse       []Point
	Depth         int
	dataSize      int
	data          []Point
	lines         [][]Point
	Bounds        pixel.Rect
	colorTab      []pixel.RGBA
}

// Changed causes re-rendering of a fractal.
func (f *Fractal) Changed() {
	// compute an inverted base.
	// first point is the last point's non-position values, and the next-to-last point's
	// location, with X flipped around 0-1, etcetera, last point is the first point's
	// values and {1, 0}
	prev := pixel.Vec{}
	f.Inverse = make([]Point, len(f.Base))
	for i, p := range f.Base {
		p.Vec, prev = pixel.Vec{X: 1 - prev.X, Y: prev.Y}, p.Vec
		f.Inverse[len(f.Base)-1-i] = p
	}
	f.Depth = 0
	f.Bounds = pixel.Rect{Min: pixel.Vec{}, Max: pixel.Vec{X: 1}}
	f.Render(0)
	f.Render(1)
	f.Render(2)
	f.Render(3)
	f.Render(4)
	f.Render(5)
}

// BoundsAt allows us to compute partial bounds for a given tier.
func (f *Fractal) BoundsAt(depth int) (r pixel.Rect) {
	r.Min.X, r.Min.Y, r.Max.X, r.Max.Y = 0, 0, 1, 0
	for _, p := range f.lines[depth] {
		if p.X < r.Min.X {
			r.Min.X = p.X
		} else if p.X > r.Max.X {
			r.Max.X = p.X
		}
		if p.Y < r.Min.Y {
			r.Min.Y = p.Y
		} else if p.Y > r.Max.Y {
			r.Max.Y = p.Y
		}
	}
	return
}

// AdjustedBounds produces the current bounds, adjusted to the aspect ratio
// of r0, and scaled by a scale factor.
func (f *Fractal) AdjustedBounds(r0 pixel.Rect, scale int32) (r pixel.Rect) {
	portRatio := r0.W() / r0.H()
	r = f.Bounds
	size := r.Size()
	var dx, dy float64
	if size.Y == 0 || (size.X/size.Y) > portRatio {
		dy = (size.X / portRatio) - size.Y
		r.Min.Y -= dy / 2
		r.Max.Y += dy / 2
	} else {
		dx = (size.Y * portRatio) - size.X
		r.Min.X -= dx / 2
		r.Max.X += dx / 2
	}
	if scale != 0 {
		dx, dy = r.Size().XY()
		scaleFactor := math.Pow(0.95, float64(scale))
		dx *= scaleFactor - 1
		dy *= scaleFactor - 1
		r.Min.X -= dx / 2
		r.Max.X += dx / 2
		r.Min.Y -= dy / 2
		r.Max.Y += dy / 2
	}
	return
}

// NewAffineBetween gives an affine transform that maps [0,0]->[1,0] onto the line segment between the given points.
func NewAffineBetween(p0, p1 Point) pixel.Matrix {
	dx, dy := p1.X-p0.X, p1.Y-p0.Y

	scale := math.Sqrt(dx*dx + dy*dy)
	theta := math.Atan2(dy, dx)
	sint, cost := math.Sincos(theta)

	return pixel.Matrix{scale * cost, scale * sint, -scale * sint, scale * cost, p0.X, p0.Y}
	// x1 x2 x0   x   x'
	// y1 y2 y0 * y = y'
	// 0  0  1    1   1
}

// NewAffinesBetween attempts to build affine matrixes to convert linearly between
// the given Rects. It is unnecessary, because Unproject() exists.
func NewAffinesBetween(r0, r1 pixel.Rect) (to, from pixel.Matrix) {
	s0 := r0.Size()
	s1 := r1.Size()
	to = pixel.Matrix{4: r1.Min.X - (r0.Min.X * s1.X / s0.X), 5: r1.Min.Y - (r0.Min.Y * s1.Y / s0.Y), 0: s1.X / s0.X, 3: s1.Y / s0.Y}
	from = pixel.Matrix{4: r0.Min.X - (r1.Min.X * s0.X / s1.X), 5: r0.Min.Y - (r1.Min.Y * s0.Y / s1.Y), 0: s0.X / s1.X, 3: s0.Y / s1.Y}

	return
}

// Alloc reallocates the fractal's point/line storage, and should be needed
// only when the number of points at each depth changes.
func (f *Fractal) Alloc() {
	f.MaxDepth = 20
	totals := make([]int, f.MaxDepth)
	total := 0
	size := 1
	for i := 0; i < f.MaxDepth; i++ {
		total += size
		totals[i] = total
		size *= len(f.Base)
		// cap maxdepth
		if total+size > (1 << f.MaxOOM) {
			f.MaxDepth = i + 1
		}
	}
	f.Total = total
	f.lines = make([][]Point, f.MaxDepth)
	prev := 0
	fmt.Printf("%d points, %d depth, %d total size.\n", len(f.Base), f.MaxDepth, total)
	for i := 0; i < f.MaxDepth; i++ {
		// fmt.Printf("depth %d: %d to %d\n", i, prev, totals[i])
		f.lines[i] = f.data[prev:totals[i]]
		prev = totals[i]
	}
	f.Changed()
}

// NewFractal allocates a fractal.
func NewFractal(base []Point, maxOOM uint) *Fractal {
	f := new(Fractal)
	f.Base = base[:]
	f.selectedPoint = -1
	f.MaxOOM = maxOOM
	f.data = make([]Point, 1<<f.MaxOOM, 1<<f.MaxOOM)
	// special case: The first depth is automatic.
	f.data[0] = Point{Vec: pixel.Vec{X: 1, Y: 0}}
	// this will be capped by MaxOOM
	f.Depth = 1
	f.colorTab = make([]pixel.RGBA, 1024)
	for i := range f.colorTab {
		h := int16((i * 360) / 1024)
		s := int16(255)
		v := int16(255)
		r, g, b := rgb(h, s, v)
		f.colorTab[i] = pixel.RGBA{R: float64(r) / 255, G: float64(g) / 255, B: float64(b) / 255, A: 1}
		// if i % 64 == 0 {
		//	fmt.Printf("%d: %d, %d, %d => %d, %d, %d\n",
		//		i, int(h), int(s), int(v), int(r), int(g), int(b))
		//}
	}
	f.Alloc()
	jsonstr, err := json.Marshal(*f)
	if err == nil {
		fmt.Printf("json: %s\n", jsonstr)
	}
	return f
}

// Toggle toggles the selected flag bit
func (f *Fractal) Toggle(flag int) {
	if f.selectedPoint < 0 || f.selectedPoint >= len(f.Base) {
		return
	}
	f.Base[f.selectedPoint].Flags ^= flag
	f.SelectPoint(f.selectedPoint)
	f.Changed()
}

// ColorChange adds an amount to the color trait of the point.
func (f *Fractal) ColorChange(amt int) {
	if f.selectedPoint < 0 || f.selectedPoint >= len(f.Base) {
		return
	}
	f.Base[f.selectedPoint].Color += int16(amt)
	f.Base[f.selectedPoint].Color %= 1024
	f.SelectPoint(f.selectedPoint)
	f.Changed()
}

// XChange adds an amount to the innate color trait of the point.
func (f *Fractal) XChange(amt float64) {
	if f.selectedPoint < 0 || f.selectedPoint >= len(f.Base) {
		return
	}
	f.Base[f.selectedPoint].X += amt
	f.SelectPoint(f.selectedPoint)
	f.Changed()
}

// XChange adds an amount to the innate color trait of the point.
func (f *Fractal) YChange(amt float64) {
	if f.selectedPoint < 0 || f.selectedPoint >= len(f.Base) {
		return
	}
	f.Base[f.selectedPoint].Y += amt
	f.SelectPoint(f.selectedPoint)
	f.Changed()
}

// AddPoint divides the line segment ending in the currently selected point in half.
func (f *Fractal) AddPoint() {
	// cap size
	if len(f.Base) >= 6 || f.selectedPoint < 0 || f.selectedPoint >= len(f.Base) {
		return
	}
	newbase := make([]Point, len(f.Base)+1)
	j := 0
	prev := Point{}
	for i, p := range f.Base {
		if i == f.selectedPoint {
			newPoint := p
			newPoint.X = (p.X + prev.X) / 2
			newPoint.Y = (p.Y + prev.Y) / 2
			newbase[j] = newPoint
			// fmt.Printf("New point[%d]: %.3f, %.3f\n", j, p.X, p.Y)
			j++
		}
		newbase[j] = p
		j++
		prev = p
	}
	f.Base = newbase
	f.Alloc()
}

// DelPoint deletes the currently selected point.
func (f *Fractal) DelPoint() {
	// cap size
	if len(f.Base) < 3 || f.selectedPoint < 0 || f.selectedPoint > len(f.Base) {
		return
	}
	newbase := make([]Point, len(f.Base)-1)
	j := 0
	for i, p := range f.Base {
		if i != f.selectedPoint {
			newbase[j] = p
			j++
		}
	}
	f.Base = newbase
	f.Alloc()
}

// Points returns the points for a given depth. This is trivial except for
// the hackery to make depth 0 work. It's probably wrong.
func (f *Fractal) Points(depth int) []Point {
	if depth > f.Depth || depth < 0 {
		return nil
	}
	if depth == 0 {
		return []Point{Point{Vec: pixel.Vec{X: 1, Y: 0}}}
	}
	return f.lines[depth]
}

// Render computes the points for a given depth, if the previous line is filled in.
func (f *Fractal) Render(depth int) bool {
	var src []Point
	// the 0-depth case is already filled in, but we need to fix color for it
	if depth == 0 {
		return true
	}
	if depth == 1 {
		dest := f.lines[depth]
		for i := range f.Base {
			dest[i] = f.Base[i]
			if dest[i].Flags&FixedC != 0 {
				dest[i].Color = modPlus(dest[i].Color, 1024)
			} else {
				dest[i].Color = 0
			}
		}
		if f.Depth < 1 {
			f.Depth = 1
		}
		return true
	}
	if depth > 0 && depth < f.MaxDepth {
		src = f.Points(depth - 1)
	}
	if src == nil {
		return false
	}
	dest := f.lines[depth]
	offset := 0
	l := len(f.Base)

	// fmt.Printf("render depth %d (src %d, dest %d points)\n", depth, len(src), cap(dest))

	prev := Point{}
	for p := range src {
		// fmt.Printf("rendering partial %d [%d:%d]\n", p, offset, offset + l)
		f.Partial(prev, src[p], dest[offset:offset+l])
		prev = src[p]
		offset += l
	}
	nb := f.BoundsAt(depth)
	f.Bounds = f.Bounds.Union(nb)

	if f.Depth < depth {
		f.Depth = depth
	}
	return true
}

// Partial computes the points interpolated from a single point pair.
func (f *Fractal) Partial(p0 Point, p1 Point, dest []Point) {
	flipY := p1.Flags&FlipY != 0
	flipX := p1.Flags&FlipX != 0
	a := NewAffineBetween(p0, p1)
	var base []Point
	if flipX {
		base = f.Inverse
	} else {
		base = f.Base
	}

	for i := 0; i < len(base); i++ {
		p := base[i]
		if flipY {
			p.Y *= -1
		}
		dest[i] = p
		dest[i].Vec = a.Project(p.Vec)
		if p.Flags&FixedC == 0 {
			dest[i].Color += p1.Color
		}
		dest[i].Color = modPlus(dest[i].Color, 1024)
		dest[i].Flags ^= (p1.Flags & (FlipX | FlipY))
		// fmt.Printf("... point %d: %v\n", i, dest[i])
	}
}

func loadTTF(path string, size float64) (font.Face, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, err
	}

	font, err := truetype.Parse(bytes)
	if err != nil {
		return nil, err
	}

	return truetype.NewFace(font, &truetype.Options{
		Size:              size,
		GlyphCacheEntries: 1,
	}), nil
}

var runningMutex sync.Mutex

// modPlus yields the positive remainder of x/y
func modPlus(x, y int16) int16 {
	x = x % y
	if x < 0 {
		return x + y
	}
	return x
}

// satMod(x,y) yields y-1 for x >= y, otherwise the positive remainder of x/y
func satMod(x, y int16) int16 {
	if x >= y {
		return y - 1
	}
	if x < 0 {
		return x%y + y
	}
	return x
}

// r, g, b = rgb(frac[i].h, frac[i].s, frac[i].v)
func rgb(h, s, v int16) (r, g, b int16) {
	h = modPlus(h, 360)
	q := h / 60
	hp := h % 60
	s = satMod(s, 256)
	v = satMod(v, 256)
	c := int16(int(s) * int(v) / 255)
	m := v - c
	x := c * hp / 60
	if (q & 1) != 0 {
		x = c - x
	}
	switch q {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	case 5:
		r, g, b = c, 0, x
	}
	r, g, b = r+m, g+m, b+m
	return
}

var frac *Fractal

// UIFlag sets the field with a given label to reflect a point's boolean flags. It's wrong.
func (p *Point) UIFlag(label string, flag int) {
	e := UIElements[label]
	if e != nil {
		e.BoolColor(p.Flags&flag != 0)
		e.SetEnabled(true)
	}
}

// SelectPoint marks a given point as the current selected point, populating UI fields.
func (f *Fractal) SelectPoint(index int) {
	if index >= 0 && index < len(f.Base) {
		f.selectedPoint = index
		p := f.Base[index]
		p.UIFlag("FlipX", FlipX)
		p.UIFlag("FlipY", FlipY)
		p.UIFlag("Hide", Hide)
		p.UIFlag("Prune", Prune)
		p.UIFlag("FixC", FixedC)
		pointElements.SetHidden(false)
	} else {
		f.selectedPoint = -1
		pointElements.SetHidden(true)
	}
}

func init() {
	var err error

	face, err = loadTTF("Go-Mono.ttf", 18)
	if err != nil {
		log.Fatal(err)
	}
	atlas = text.NewAtlas(face, text.ASCII)

	textRenderer = text.New(pixel.Vec{}, atlas)

	textMatrix = pixel.Matrix{
		0: atlas.Glyph(' ').Advance,
		3: -atlas.LineHeight(),
		5: 800 - atlas.LineHeight(),
	}
}

// UIElement describes a UI widget that is probably some kind of button.
type UIElement struct {
	bounds    pixel.Rect
	matrix    pixel.Matrix
	callback  func()
	sprite    *pixel.Sprite
	color     pixel.RGBA
	baseColor pixel.RGBA
	label     string
	dimmed    bool
	state     int
	enabled   bool
	hidden    bool
}

// UIBatch is a set of related UI elements, for purposes like hiding/not-hiding.
type UIBatch []*UIElement

// UIElements is the set of UI elements, and shouldn't be exported.
var (
	buttonCanvasSize = 512.0
	buttonCanvas     *pixelgl.Canvas
	buttonDot        pixel.Vec
	UIElements       map[string]*UIElement
	pointElements    UIBatch
	uiBatch          *pixel.Batch
)

func init() {
	UIElements = make(map[string]*UIElement)
}

// SetColor changes the base color of a UI element.
func (u *UIElement) SetColor(color pixel.RGBA) {
	u.baseColor = color
	u.Colorize()
}

// BoolColor sets a flag field to green (true) or blue (false)
func (u *UIElement) BoolColor(flag bool) {
	if flag {
		u.SetColor(pixel.RGBA{R: 0, G: .7, B: 0, A: 1})
	} else {
		u.SetColor(pixel.RGBA{R: 0, G: 0, B: 1, A: 1})
	}
}

// Colorize sets the actual color based on the dimmed and enabled fields.
// Probably there should be a backdrop which is affected separately.
func (u *UIElement) Colorize() {
	scale := 1.0
	if u.dimmed {
		scale *= 0.7
	}
	if !u.enabled {
		scale *= 0.5
	}
	u.color = u.baseColor.Scaled(scale)
}

// SetEnabled sets the Enabled flag. Surprising!
func (u *UIElement) SetEnabled(state bool) {
	u.enabled = state
	if !u.enabled {
		u.Unpressed()
	}
	u.Colorize()
}

// SetDimmed alters a boolean flag. Why did I make this.
func (u *UIElement) SetDimmed(state bool) {
	u.dimmed = state
	u.Colorize()
}

// SetHidden determines whether or not to hide the element.
func (u *UIElement) SetHidden(state bool) {
	u.hidden = state
}

// Press handles the state transition to Pressed.
func (u *UIElement) Press() {
	u.SetDimmed(true)
	u.state = Pressed
}

// Unpressed handles the state transition to Unpressed, and does not call a callback.
func (u *UIElement) Unpressed() {
	u.SetDimmed(false)
	u.state = Unpressed
}

// Release handles the state transition to Unpressed, but *does* call the callback.
func (u *UIElement) Release() {
	u.SetDimmed(false)
	u.state = Unpressed
	u.callback()
}

func button(at pixel.Vec, name string, callback func(), format string, args ...interface{}) *UIElement {
	descent := atlas.Descent()
	if buttonCanvas == nil {
		buttonCanvas = pixelgl.NewCanvas(pixel.Rect{Min: pixel.Vec{}, Max: pixel.Vec{X: buttonCanvasSize, Y: buttonCanvasSize}})
		// buttonCanvas.Clear(pixel.RGBA{0, .2, 0, 1})
		uiBatch = pixel.NewBatch(&pixel.TrianglesData{}, buttonCanvas)
		buttonDot.Y = descent
	}
	textRenderer.Clear()
	textRenderer.Color = pixel.RGBA{R: 1, G: 1, B: 1, A: 1}
	textRenderer.Orig = pixel.Vec{X: 0, Y: descent}
	textRenderer.Dot = textRenderer.Orig
	label := fmt.Sprintf(format, args...)
	textSize := textRenderer.BoundsOf(label)
	if textSize.Max.X+buttonDot.X > buttonCanvasSize {
		buttonDot.Y += textSize.Max.Y + descent + 2
		buttonDot.X = 0
	}
	textRenderer.Orig = buttonDot
	textRenderer.Dot = textRenderer.Orig
	textRenderer.WriteString(label)
	textRenderer.Draw(buttonCanvas, pixel.IM)
	buttonDot.X += textSize.W() + 2
	spriteBounds := textRenderer.Bounds()
	sprite := pixel.NewSprite(buttonCanvas, spriteBounds)
	at = textMatrix.Project(at)
	// draw instruction will center on point, rather than originating at point
	center := at.Add(spriteBounds.Size().Scaled(0.5))
	btn := &UIElement{
		enabled:   true,
		color:     pixel.RGBA{R: 1, G: 1, B: 1, A: 1},
		baseColor: pixel.RGBA{R: 1, G: 1, B: 1, A: 1},
		bounds:    pixel.Rect{Min: at, Max: spriteBounds.Size().Add(at)},
		callback:  callback,
		sprite:    sprite,
		matrix:    pixel.IM.Moved(center),
		label:     label,
	}
	UIElements[name] = btn
	return btn
}

func textAt(t pixel.Target, at pixel.Vec, color pixel.RGBA, format string, args ...interface{}) pixel.Rect {
	at = textMatrix.Project(at)
	textRenderer.Clear()
	textRenderer.Color = color
	textRenderer.Orig = at
	textRenderer.Dot = textRenderer.Orig
	fmt.Fprintf(textRenderer, format, args...)
	bounds := textRenderer.Bounds().Moved(at)
	textRenderer.Draw(t, pixel.IM)
	return bounds
}

func (u UIElement) String() string {
	return fmt.Sprintf("[%s]", u.label)
}

// SetHidden hides or unhides the elements in the batch.
func (us UIBatch) SetHidden(state bool) {
	for _, u := range us {
		u.SetHidden(state)
	}
}

func run() {
	var err error

	var (
		frames       int
		lastFPS      int
		totalFrames  int
		averageFPS   float64
		totalSeconds int
		dragging     bool
		dragStart    pixel.Vec
		dragPoint    pixel.Vec
		lastDrag     pixel.Vec
		winScale     = pixel.Vec{X: 1000, Y: 800}
		canScale     = 2.0
		margin       = 5.0
	)

	f, err := os.Create("pdata")
	if err != nil {
		log.Fatal(err)
	}
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	fracPortRect := pixel.Rect{Min: pixel.Vec{X: margin, Y: margin}, Max: winScale.Scaled(canScale).Sub(pixel.Vec{X: margin, Y: margin})}
	fracPortScale := int32(0)
	base := []Point{
		Point{pixel.Vec{X: 0.05, Y: 0.25}, 0, 0},
		Point{pixel.Vec{X: 0.95, Y: -0.25}, 0, 128},
		Point{pixel.Vec{X: 1, Y: 0}, 0, 256},
	}
	frac = NewFractal(base, 18)
	for i := 0; i < frac.MaxDepth; i++ {
		if !frac.Render(i) {
			fmt.Printf("oops, render %d failed.\n", i)
		}
	}
	fracRect := frac.AdjustedBounds(fracPortRect, fracPortScale)
	fracMatrix, _ := NewAffinesBetween(fracRect, fracPortRect)

	cfg := pixelgl.WindowConfig{
		Title:  "Pixel Rocks!",
		Bounds: pixel.R(0, 0, 1200, 800),
		VSync:  true,
	}
	win, err := pixelgl.NewWindow(cfg)
	if err != nil {
		panic(err)
	}
	win.SetSmooth(true)

	can := pixelgl.NewCanvas(pixel.Rect{Min: pixel.Vec{}, Max: pixel.Vec{X: 2000, Y: 1600}})
	win.SetComposeMethod(pixel.ComposePlus)
	canMatrix := pixel.IM.Scaled(pixel.Vec{}, 0.5).Moved(pixel.Vec{X: 700, Y: 400})

	imd := imdraw.New(nil)
	imd.SetMatrix(fracMatrix)

	second := time.Tick(time.Second)
	button(pixel.Vec{X: 0, Y: 4}, "AddPoint", func() { frac.AddPoint() }, "Add")
	button(pixel.Vec{X: 6, Y: 4}, "DelPoint", func() { frac.DelPoint() }, "Del")
	pointElements = append(pointElements, button(pixel.Vec{X: 0, Y: 15}, "FlipX", func() { frac.Toggle(FlipX) }, "FlipX"))
	pointElements = append(pointElements, button(pixel.Vec{X: 8, Y: 15}, "FlipY", func() { frac.Toggle(FlipY) }, "FlipY"))
	pointElements = append(pointElements, button(pixel.Vec{X: 00, Y: 16}, "Hide", func() { frac.Toggle(Hide) }, "Hide"))
	pointElements = append(pointElements, button(pixel.Vec{X: 8, Y: 16}, "Prune", func() { frac.Toggle(Prune) }, "Prune"))
	pointElements = append(pointElements, button(pixel.Vec{X: 0, Y: 17}, "FixC", func() { frac.Toggle(FixedC) }, "FixC"))
	pointElements = append(pointElements, button(pixel.Vec{X: 0, Y: 9}, "<<", func() { frac.ColorChange(-16) }, "<<"))
	pointElements = append(pointElements, button(pixel.Vec{X: 3, Y: 9}, "<", func() { frac.ColorChange(-1) }, "<"))
	pointElements = append(pointElements, button(pixel.Vec{X: 5, Y: 9}, ">", func() { frac.ColorChange(1) }, ">"))
	pointElements = append(pointElements, button(pixel.Vec{X: 7, Y: 9}, ">>", func() { frac.ColorChange(16) }, ">>"))
	pointElements = append(pointElements, button(pixel.Vec{X: 10, Y: 6}, "+X", func() { frac.XChange(-.005) }, "<"))
	pointElements = append(pointElements, button(pixel.Vec{X: 12, Y: 6}, "-X", func() { frac.XChange(.005) }, ">"))
	pointElements = append(pointElements, button(pixel.Vec{X: 10, Y: 7}, "+Y", func() { frac.YChange(-.005) }, "<"))
	pointElements = append(pointElements, button(pixel.Vec{X: 12, Y: 7}, "-Y", func() { frac.YChange(.005) }, ">"))
	frac.SelectPoint(-1)

	for !win.Closed() {
		scrolled := win.MouseScroll()
		mousePos := win.MousePosition()
		canPos := canMatrix.Unproject(mousePos).Add(pixel.Vec{X: 1000, Y: 800})
		if scrolled.Y != 0 {
			fracPortScale += int32(scrolled.Y)
			if !dragging {
				fracRect = frac.AdjustedBounds(fracPortRect, fracPortScale)
				fracMatrix, _ = NewAffinesBetween(fracRect, fracPortRect)
				imd.SetMatrix(fracMatrix)
			}
		}
		if win.JustPressed(pixelgl.MouseButtonLeft) {
			found := false
			for _, element := range UIElements {
				if element.bounds.Contains(mousePos) && element.enabled {
					element.Press()
					found = true
					break
				}
			}

			if !found && canPos.X >= 0 {
				// find click within the canvas space
				leastDist := 999999.0
				pidx := -1
				for i, p := range frac.Base {
					pv := fracMatrix.Project(pixel.Vec{X: p.X, Y: p.Y})
					dist := math.Hypot(pv.X-canPos.X, pv.Y-canPos.Y)
					if dist < 30 && dist < leastDist {
						leastDist = dist
						pidx = i
					}
				}
				frac.SelectPoint(pidx)
				if pidx > -1 {
					dragStart = fracMatrix.Unproject(canPos)
					dragPoint = frac.Base[pidx].Vec
					lastDrag = dragPoint
					dragging = true
				}
			}
		} else if win.JustReleased(pixelgl.MouseButtonLeft) {
			for _, element := range UIElements {
				if element.state == Pressed {
					// if we find an element which is in Pressed
					// state, and we are not pressing it, let it know.
					if element.bounds.Contains(mousePos) {
						element.Release()
					} else {
						element.Unpressed()
					}
				}
			}
			if dragging {
				fracRect = frac.AdjustedBounds(fracPortRect, fracPortScale)
				fracMatrix, _ = NewAffinesBetween(fracRect, fracPortRect)
				imd.SetMatrix(fracMatrix)
			}
			dragging = false
		}
		if dragging {
			current := fracMatrix.Unproject(canPos)
			if current != lastDrag {
				if frac.selectedPoint >= 0 && frac.selectedPoint < len(frac.Base) {
					frac.Base[frac.selectedPoint].Vec = dragPoint.Add(current.Sub(dragStart))
					frac.Changed()
				}
				lastDrag = current
			}
		}
		if frac.Depth < frac.MaxDepth-1 && !dragging {
			frac.Render(frac.Depth + 1)
			fracRect = frac.AdjustedBounds(fracPortRect, fracPortScale)
			fracMatrix, _ = NewAffinesBetween(fracRect, fracPortRect)
			imd.SetMatrix(fracMatrix)
		}
		win.SetComposeMethod(pixel.ComposeOver)
		win.Clear(pixel.RGBA{R: 0, G: 0, B: 0, A: 255})
		textAt(win, pixel.Vec{X: 0, Y: 0}, pixel.RGBA{R: 1, G: 1, B: 1, A: 1},
			"Scale: %d", fracPortScale)
		textAt(win, pixel.Vec{X: 0, Y: 1}, pixel.RGBA{R: 1, G: 1, B: 1, A: 1},
			"Depth: %d/%d", frac.Depth, frac.MaxDepth-1)
		textAt(win, pixel.Vec{X: 0, Y: 2}, pixel.RGBA{R: 1, G: 1, B: 1, A: 1},
			"Points: %d", frac.Total)
		uiBatch.Clear()
		for _, e := range UIElements {
			if !e.hidden {
				e.sprite.DrawColorMask(uiBatch, e.matrix, e.color)
			}
		}
		uiBatch.Draw(win)
		if frac.selectedPoint >= 0 {
			p := frac.Base[frac.selectedPoint]
			textAt(win, pixel.Vec{X: 0, Y: 5}, pixel.RGBA{R: .7, G: .7, B: .7, A: 1},
				"Point: %d", frac.selectedPoint+1)
			textAt(win, pixel.Vec{X: 0, Y: 6}, pixel.RGBA{R: .7, G: .7, B: .7, A: 1},
				"X: %-+6.3f", p.X)
			textAt(win, pixel.Vec{X: 0, Y: 7}, pixel.RGBA{R: .7, G: .7, B: .7, A: 1},
				"Y: %-+6.3f", p.Y)
			col := modPlus(p.Color, 1024)
			textAt(win, pixel.Vec{X: 0, Y: 8}, frac.colorTab[col], "Color: %d", p.Color)
		}
		textAt(win, pixel.Vec{X: 0, Y: 30}, pixel.RGBA{R: 1, G: 1, B: 1, A: 1},
			"FPS: %d\n[%.1f avg %ds]", lastFPS, averageFPS, totalSeconds)
		can.Clear(pixel.RGBA{R: 0, G: 0, B: 0, A: 255})
		can.Draw(win, canMatrix)
		win.SetComposeMethod(pixel.ComposePlus)
		for i := 1; i <= frac.Depth; i++ {
			imd.Clear()
			points := frac.Points(i)
			last := len(points) - 1
			imd.Color = frac.colorTab[points[last].Color]
			imd.Push(pixel.Vec{})
			imd.Color = frac.colorTab[points[0].Color]
			imd.Push(points[0].Vec)
			for j := 1; j < len(points); j++ {
				imd.Color = frac.colorTab[points[j].Color]
				imd.Push(points[j].Vec)
			}
			imd.Line(2 / fracMatrix[0])
			imd.Draw(can)
			can.Draw(win, canMatrix)
			can.Clear(pixel.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
		if frac.selectedPoint >= 0 {
			line := frac.Points(1)
			p := line[frac.selectedPoint]
			imd.Clear()
			if frac.selectedPoint > 0 {
				imd.Color = frac.colorTab[line[frac.selectedPoint-1].Color]
				imd.Push(line[frac.selectedPoint-1].Vec)
			} else {
				imd.Color = frac.colorTab[line[len(line)-1].Color]
				imd.Push(pixel.Vec{})
			}
			imd.Color = frac.colorTab[p.Color]
			imd.Push(p.Vec)
			imd.Line(6 / fracMatrix[0])
			imd.Draw(can)
			can.Draw(win, canMatrix)
			can.Clear(pixel.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
		win.Update()
		frames++
		select {
		case <-second:
			lastFPS = frames
			totalFrames += frames
			frames = 0
			totalSeconds++
			averageFPS = float64(totalFrames) / float64(totalSeconds)
		default:
		}

	}
	fmt.Printf("Average FPS: %.1f\n", averageFPS)
}

func main() {
	pixelgl.Run(run)
}
