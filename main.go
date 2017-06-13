package main

import (
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
	FixedH
	FixedS
	FixedV
)

const (
	Released = iota
	Pressed
	Unpressed
	Dragging
)

var (
	face         font.Face
	atlas        *text.Atlas
	textRenderer *text.Text
	textMatrix   pixel.Matrix
)

type Point struct {
	pixel.Vec
	Flags   int
	H, S, V uint16 // 0-360, 0-255, 0-255
}

func (p Point) GoString() string {
	return fmt.Sprintf("%.3f, %.3f, 0x%03x, %d/%d/%d", p.X, p.Y, p.Flags, p.H, p.S, p.V)
}

type Fractal struct {
	dataSize int
	MaxDepth int
	Depth    int
	MaxOOM   uint
	Total    int
	Base     []Point
	data     []Point
	lines    [][]Point
	Bounds   pixel.Rect
	H, S, V  uint16 // 0-360, 0-255, 0-255
}

// indicate that we have to redraw
func (f *Fractal) Changed() {
	f.Depth = 0
	f.Bounds = pixel.Rect { Min: pixel.Vec{}, Max: pixel.Vec{1, 0} }
	f.Render(0)
	f.Render(1)
	f.Render(2)
	f.Render(3)
	f.Render(4)
	f.Render(5)
}

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

func NewAffinesBetween(r0, r1 pixel.Rect) (to, from pixel.Matrix) {
	s0 := r0.Size()
	s1 := r1.Size()
	to = pixel.Matrix{4: r1.Min.X - (r0.Min.X * s1.X / s0.X), 5: r1.Min.Y - (r0.Min.Y * s1.Y / s0.Y), 0: s1.X / s0.X, 3: s1.Y / s0.Y}
	from = pixel.Matrix{4: r0.Min.X - (r1.Min.X * s0.X / s1.X), 5: r0.Min.Y - (r1.Min.Y * s0.Y / s1.Y), 0: s0.X / s1.X, 3: s0.Y / s1.Y}

	return
}

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

func NewFractal(base []Point, maxOOM uint) *Fractal {
	f := new(Fractal)
	f.Base = base[:]
	f.MaxOOM = maxOOM
	f.data = make([]Point, 1<<f.MaxOOM, 1<<f.MaxOOM)
	// special case: The first depth is automatic.
	f.data[0] = Point{Vec: pixel.Vec{X: 1, Y: 0}}
	// this will be capped by MaxOOM
	f.Depth = 1
	f.Alloc()
	return f
}

func (f *Fractal) AddPoint(beforePoint int) {
	// cap size
	if len(f.Base) >= 6 || beforePoint < 0 || beforePoint > len(f.Base) {
		return
	}
	newbase := make([]Point, len(f.Base)+1)
	j := 0
	prev := Point{}
	for i, p := range f.Base {
		if i == beforePoint {
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

func (f *Fractal) DelPoint(point int) {
	// cap size
	if len(f.Base) < 2 || point < 0 || point > len(f.Base) {
		return
	}
	newbase := make([]Point, len(f.Base)-1)
	j := 0
	for i, p := range f.Base {
		if i != point {
			newbase[j] = p
			j++
		}
	}
	f.Base = newbase
	f.Alloc()
}

func (f *Fractal) Points(depth int) []Point {
	if depth > f.Depth || depth < 0 {
		return nil
	}
	if depth == 0 {
		return []Point{Point{Vec: pixel.Vec{X: 1, Y: 0}, H: 330}}
	}
	return f.lines[depth]
}

func (f *Fractal) Render(depth int) bool {
	var src []Point
	// the 0-depth case is already filled in, but we need to fix color for it
	if depth == 0 {
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

func (f *Fractal) Partial(p0 Point, p1 Point, dest []Point) {
	a := NewAffineBetween(p0, p1)

	for i := 0; i < len(f.Base); i++ {
		p := f.Base[i]
		dest[i].Vec = a.Project(p.Vec)
		dest[i].H = (p.H + p1.H + (f.H / uint16(f.MaxDepth))) % 360
		dest[i].S = p.S
		dest[i].V = p.V
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

// r, g, b = rgb(frac[i].h, frac[i].s, frac[i].v)
func rgb(h, s, v uint16) (r, g, b uint16) {
	h = (h + 720) % 360
	q := h / 60
	hp := h % 60
	c := s * v / 255
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

var selectedPoint = -1
var frac *Fractal

func selectPoint(p int) {
	if p >= 0 && p < len(frac.Base) {
		selectedPoint = p
	} else {
		selectedPoint = -1
	}
}

func dist(x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	return math.Sqrt(dx*dx + dy*dy)
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
}

var (
	buttonCanvasSize = 512.0
	buttonCanvas     *pixelgl.Canvas
	buttonDot	 pixel.Vec
	UIElements       map[string]*UIElement
	uiBatch          *pixel.Batch
)

func init() {
	UIElements = make(map[string]*UIElement)
}

func (u *UIElement) SetColor(color pixel.RGBA) {
	u.baseColor = color
	u.Dim(u.dimmed)
}

func (u *UIElement) Dim(state bool) {
	u.dimmed = state
	if u.dimmed {
		u.color = u.baseColor.Scaled(0.7)
	} else {
		u.color = u.baseColor
	}
}

func (u *UIElement) Press() {
	u.Dim(true)
	u.state = Pressed
}

func (u *UIElement) Unpressed() {
	u.Dim(false)
	u.state = Unpressed
}

func (u *UIElement) Release() {
	u.Dim(false)
	u.state = Unpressed
	u.callback()
}

func button(at pixel.Vec, name string, callback func(), format string, args ...interface{}) {
	descent := atlas.Descent()
	if buttonCanvas == nil {
		buttonCanvas = pixelgl.NewCanvas(pixel.Rect { Min: pixel.Vec { 0, 0 }, Max: pixel.Vec { buttonCanvasSize, buttonCanvasSize } })
		buttonCanvas.Clear(pixel.RGBA{0, .2, 0, 1})
		uiBatch = pixel.NewBatch(&pixel.TrianglesData{}, buttonCanvas)
		buttonDot.Y = descent
	}
	textRenderer.Clear()
	textRenderer.Color = pixel.RGBA{1, 1, 1, 1}
	textRenderer.Orig = pixel.Vec{0, descent}
	textRenderer.Dot = textRenderer.Orig
	label := fmt.Sprintf(format, args...)
	textSize := textRenderer.BoundsOf(label)
	if textSize.Max.X + buttonDot.X > buttonCanvasSize {
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
	// draw instruction will center on point, rather than originating at point
	center := at.Add(spriteBounds.Size().Scaled(0.5))
	UIElements[name] = &UIElement{
		color: pixel.RGBA{1, 1, 1, 1},
		baseColor: pixel.RGBA{1, 1, 1, 1},
		bounds: pixel.Rect{Min: at, Max: spriteBounds.Size().Add(at) },
		callback: callback,
		sprite: sprite,
		matrix: pixel.IM.Moved(center),
		label: label,
	}
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

func run() {
	var err error

	var (
		frames         int
		lastFPS        int
		totalFrames    int
		averageFPS     float64
		totalSeconds   int
		dragging       bool
		dragStart      pixel.Vec
		dragPoint      pixel.Vec
		lastDrag       pixel.Vec
		winScale     = pixel.Vec { X: 1000, Y: 800 }
		canScale     = 2.0
		margin	     = 5.0
	)

	f, err := os.Create("pdata")
	if err != nil {
		log.Fatal(err)
	}
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	fracPortRect := pixel.Rect{Min: pixel.Vec{margin, margin}, Max: winScale.Scaled(canScale).Sub(pixel.Vec{margin, margin})}
	fracPortScale := int32(0)
	base := []Point{
		Point{pixel.Vec{0.05, 0.25}, 0, 0, 255, 255},
		Point{pixel.Vec{0.95, -0.25}, 0, 0, 255, 255},
		Point{pixel.Vec{1, 0}, 0, 0, 255, 255},
	}
	frac = NewFractal(base, 18)
	frac.H = 330
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

	can := pixelgl.NewCanvas(pixel.Rect{pixel.Vec{0, 0}, pixel.Vec{2000, 1600}})
	win.SetComposeMethod(pixel.ComposePlus)
	canMatrix := pixel.IM.Scaled(pixel.Vec{0, 0}, 0.5).Moved(pixel.Vec{700, 400})

	imd := imdraw.New(nil)
	imd.SetMatrix(fracMatrix)

	second := time.Tick(time.Second)

	button(pixel.Vec{30, 50}, "DelPoint", func() { frac.DelPoint(selectedPoint) }, "-")
	button(pixel.Vec{50, 50}, "AddPoint", func() { fmt.Println("y"); frac.AddPoint(selectedPoint) }, "+")

	for !win.Closed() {
		scrolled := win.MouseScroll()
		mousePos := win.MousePosition()
		canPos := canMatrix.Unproject(mousePos).Add(pixel.Vec{1000, 800})
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
				if element.bounds.Contains(mousePos) {
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
					pv := fracMatrix.Project(pixel.Vec{p.X, p.Y})
					dist := math.Hypot(pv.X-canPos.X, pv.Y-canPos.Y)
					if dist < 30 && dist < leastDist {
						leastDist = dist
						pidx = i
					}
				}
				selectPoint(pidx)
				if pidx > -1 {
					dragStart = fracMatrix.Unproject(canPos)
					dragPoint = frac.Base[pidx].Vec
					lastDrag = dragPoint
					dragging = true
				}
			}
		} else if win.JustReleased(pixelgl.MouseButtonLeft) {
			for _, element := range UIElements {
				if element.bounds.Contains(mousePos) {
					element.Release()
				} else if element.state == Pressed {
					// if we find an element which is in Pressed
					// state, and we are not pressing it, let it know.
					element.Unpressed()
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
				frac.Base[selectedPoint].Vec = dragPoint.Add(current.Sub(dragStart))
				frac.Changed()
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
		win.Clear(pixel.RGBA{0, 0, 0, 255})
		textAt(win, pixel.Vec{0, 0}, pixel.RGBA{1, 1, 1, 1},
			"Depth: %d/%d", frac.Depth, frac.MaxDepth - 1)
		textAt(win, pixel.Vec{0, 1}, pixel.RGBA{1, 1, 1, 1},
			"Points: %d", frac.Total)
		textAt(win, pixel.Vec{0, 2}, pixel.RGBA{1, 1, 1, 1},
			"Scale: %d", fracPortScale)
		uiBatch.Clear()
		for _, e := range UIElements {
			e.sprite.DrawColorMask(uiBatch, e.matrix, e.color)
		}
		uiBatch.Draw(win)
		if selectedPoint >= 0 {
			p := frac.Base[selectedPoint]
			textAt(win, pixel.Vec{0, 4}, pixel.RGBA{.7, .7, .7, 1},
				"Point: %d", selectedPoint + 1)
			textAt(win, pixel.Vec{0, 5}, pixel.RGBA{.7, .7, .7, 1},
				"X: %-+6.3f Y: %-+6.3f", p.X, p.Y)
		}
		textAt(win, pixel.Vec{0, 30}, pixel.RGBA{1, 1, 1, 1},
			"FPS: %d\n[%.1f avg %ds]", lastFPS, averageFPS, totalSeconds)
		can.Clear(pixel.RGBA{0, 0, 0, 255})
		can.Draw(win, canMatrix)
		win.SetComposeMethod(pixel.ComposePlus)
		for i := 1; i <= frac.Depth; i++ {
			imd.Clear()
			points := frac.Points(i)
			r, g, b := rgb(points[0].H, points[0].S, points[0].V)
			imd.Color = pixel.RGBA{float64(r) / 255, float64(g) / 255, float64(b) / 255, 1}
			imd.Push(pixel.Vec{})
			for j := 0; j < len(points); j++ {
				p := points[j]
				imd.Push(pixel.Vec{p.X, p.Y})
			}
			imd.Line(2 / fracMatrix[0])
			imd.Draw(can)
			can.Draw(win, canMatrix)
			can.Clear(pixel.RGBA{0, 0, 0, 255})
		}
		if selectedPoint >= 0 {
			p := frac.Base[selectedPoint]
			imd.Clear()
			r, g, b := rgb(p.H, p.S, p.V)
			imd.Color = pixel.RGBA{float64(r) / 255, float64(g) / 255, float64(b) / 255, 1}
			if selectedPoint > 0 {
				imd.Push(pixel.Vec{frac.Base[selectedPoint-1].X, frac.Base[selectedPoint-1].Y})
			} else {
				imd.Push(pixel.Vec{})
			}
			imd.Push(pixel.Vec{p.X, p.Y})
			imd.Line(6 / fracMatrix[0])
			imd.Draw(can)
			can.Draw(win, canMatrix)
			can.Clear(pixel.RGBA{0, 0, 0, 255})
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
