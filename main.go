package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/faiface/pixel"
	"github.com/faiface/pixel/imdraw"
	"github.com/faiface/pixel/pixelgl"
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

type Coord struct {
	X, Y float64
}

type Rect struct {
	C0, C1 Coord
}

func (r Rect) GoString() string {
	return fmt.Sprintf("[%.4f, %.4f], [%.4f, %.4f]",
		r.C0.X, r.C0.Y, r.C1.X, r.C1.Y)
}

func (r Rect) Scales() (x, y float64) {
	dx, dy := r.C1.X-r.C0.X, r.C1.Y-r.C0.Y
	// arbitrarily attempt to avoid division by zero
	if dx == 0 {
		dx = -1
	}
	if dy == 0 {
		dy = -1
	}
	return math.Abs(dx), math.Abs(dy)
}

type Point struct {
	X, Y    float64
	Flags   int
	H, S, V uint16 // 0-360, 0-255, 0-255
}

func (p Point) GoString() string {
	return fmt.Sprintf("%.3f, %.3f, 0x%03x, %d/%d/%d", p.X, p.Y, p.Flags, p.H, p.S, p.V)
}

var (
	ZeroPoint = Point{X: 0, Y: 0}
	UnitLine  = []Point{Point{X: 1, Y: 0}}
)

type Fractal struct {
	dataSize int
	MaxDepth int
	Depth    int
	MaxOOM   uint
	Base     []Point
	data     []Point
	lines    [][]Point
	H, S, V  uint16 // 0-360, 0-255, 0-255
}

// indicate that we have to redraw
func (f *Fractal) Changed() {
	f.Depth = 0
	f.Render(0)
	f.Render(1)
	f.Render(2)
	f.Render(3)
	f.Render(4)
}

func (f *Fractal) Bounds() (r Rect) {
	r.C0.X, r.C0.Y, r.C1.X, r.C1.Y = 0, 0, 1, 0
	for i := 0; i <= f.Depth; i++ {
		for _, p := range f.lines[i] {
			if p.X < r.C0.X {
				r.C0.X = p.X
			} else if p.X > r.C1.X {
				r.C1.X = p.X
			}
			if p.Y < r.C0.Y {
				r.C0.Y = p.Y
			} else if p.Y > r.C1.Y {
				r.C1.Y = p.Y
			}
		}
	}
	return
}

func (f *Fractal) AdjustedBounds(r0 Rect, scale int32) (r Rect) {
	portRatio := (r0.C1.X - r0.C0.X) / (r0.C1.Y - r0.C0.Y)
	r = f.Bounds()
	// fmt.Printf("%#v\n", r)
	sx, sy := r.C1.X-r.C0.X, r.C1.Y-r.C0.Y
	if sy < .001 {
		sy = .001
	}
	var dx, dy float64
	if sy == 0 || (sx/sy) > portRatio {
		dy = (sx / portRatio) - sy
		r.C0.Y -= dy / 2
		r.C1.Y += dy / 2
	} else {
		dx = (sy * portRatio) - sx
		r.C0.X -= dx / 2
		r.C1.X += dx / 2
	}
	if scale != 0 {
		dx = r.C1.X - r.C0.X
		dy = r.C1.Y - r.C0.Y
		scaleFactor := math.Pow(0.95, float64(scale))
		sx := dx * (scaleFactor - 1)
		sy := dy * (scaleFactor - 1)
		r.C0.X -= sx / 2
		r.C1.X += sx / 2
		r.C0.Y -= sy / 2
		r.C1.Y += sy / 2
	}
	// fmt.Printf("adjusted [%.3f, %.3f, ratio %.2f vs. %.2f] %#v\n", sx, sy, sx / sy, portRatio, r)
	return
}

type Affine struct {
	// x1 x2 x0
	// y1 y2 y0
	// [0 0 1]
	X0, X1, X2, Y0, Y1, Y2 float64
}

func (a Affine) GoString() string {
	return fmt.Sprintf("[ %.4f %.4f %.4f ]\n[ %.4f %.4f %.4f ]",
		a.X1, a.X2, a.X0, a.Y1, a.Y2, a.Y0)
}

func (a Affine) Apply(x, y float64) (rx, ry float64) {
	rx = a.X1*x + a.X2*y + a.X0
	ry = a.Y1*x + a.Y2*y + a.Y0
	return
}

func (a Affine) ApplyInt(x, y float64) (rx, ry int) {
	rx = int(a.X1*x + a.X2*y + a.X0)
	ry = int(a.Y1*x + a.Y2*y + a.Y0)
	return
}

func NewAffineBetween(p0, p1 Point) Affine {
	a := Affine{X0: p0.X, Y0: p0.Y}
	dx, dy := p1.X-p0.X, p1.Y-p0.Y
	scale := math.Sqrt(dx*dx + dy*dy)
	theta := math.Atan2(dy, dx)
	cost := math.Cos(theta)
	sint := math.Sin(theta)
	// x1 x2 x0   x   x'
	// y1 y2 y0 * y = y'
	// 0  0  1    1   1
	a.X1, a.Y1, a.X2, a.Y2 = scale*cost, scale*sint, -scale*sint, scale*cost
	return a
}

func NewAffinesBetween(r0, r1 Rect) (to, from Affine) {
	sx0, sy0 := r0.Scales()
	sx1, sy1 := r1.Scales()
	to = Affine{X0: r1.C0.X - (r0.C0.X * sx1 / sx0), Y0: r1.C0.Y - (r0.C0.Y * sy1 / sy0), X1: sx1 / sx0, Y2: sy1 / sy0}
	from = Affine{X0: r0.C0.X - (r1.C0.X * sx0 / sx1), Y0: r0.C0.Y - (r1.C0.Y * sy0 / sy1), X1: sx0 / sx1, Y2: sy0 / sy1}
	// fmt.Println("affines for:")
	// fmt.Printf("%#v =>\n", r0)
	// fmt.Printf("%#v\n", r1)
	// fmt.Println("to:")
	// fmt.Printf("%#v\n", to)

	return
}

func NewAffineTo(sx, sy, ox, oy float64) Affine {
	return Affine{X0: ox, Y0: oy, X1: sx, Y2: sy}
}

func NewAffineFrom(sx, sy, ox, oy float64) Affine {
	return Affine{X0: -ox, Y0: -oy, X1: 1 / sx, Y2: 1 / sy}
}

func (f *Fractal) Alloc() {
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
	f.data = make([]Point, 1 << f.MaxOOM, 1 << f.MaxOOM)
	// special case: The first depth is automatic.
	f.data[0] = UnitLine[0]
	// this will be capped by MaxOOM
	f.MaxDepth = 99
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
		UnitLine[0].H = 360 - (f.H / uint16(f.MaxDepth))
		return UnitLine
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

	prev := ZeroPoint
	for p := range src {
		// fmt.Printf("rendering partial %d [%d:%d]\n", p, offset, offset + l)
		f.Partial(prev, src[p], dest[offset:offset+l])
		prev = src[p]
		offset += l
	}
	if f.Depth < depth {
		f.Depth = depth
	}
	return true
}

func (f *Fractal) Partial(p0 Point, p1 Point, dest []Point) {
	a := NewAffineBetween(p0, p1)

	for i := 0; i < len(f.Base); i++ {
		p := f.Base[i]
		dest[i].X, dest[i].Y = a.Apply(p.X, p.Y)
		dest[i].H = (p.H + p1.H + (f.H / uint16(f.MaxDepth))) % 360
		dest[i].S = p.S
		dest[i].V = p.V
		// fmt.Printf("... point %d: %v\n", i, dest[i])
	}
}

type wininfo struct {
	Title         string
	Width, Height int
}

const (
	FrameRate = 30
)

var MainWinInfo = wininfo{Title: "Frac", Width: 1200, Height: 800}

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

func fractish() int {
	/*
	var mouseStart Coord
	var pointStart Coord
	var dragPoint int
	var dragging bool
	for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
		switch e := event.(type) {
		case *sdl.QuitEvent:
			runningMutex.Lock()
			running = false
			runningMutex.Unlock()
		case *sdl.MouseWheelEvent:
			fracPortScale += e.Y
			fracRect = frac.AdjustedBounds(fracPortRect, fracPortScale)
			toScreen, fromScreen = NewAffinesBetween(fracRect, fracPortRect)
		case *sdl.MouseButtonEvent:
			// assume click is in fracPort
			if e.X <= fracPort.X {
				if e.Button == 1 && e.State == sdl.RELEASED {
					if u.IsClicked(e.X, e.Y) {
						fmt.Println("Clicked a thing!")
					} else {
						fmt.Println("Clicked no thing!")
					}
				}
				break
			}
			e.X -= fracPort.X
			e.Y -= fracPort.Y
			if e.Button == 1 && e.State == sdl.PRESSED {
				mouseStart.X, mouseStart.Y = fromScreen.Apply(float64(e.X), float64(e.Y))
				new := -1
				for i, p := range frac.Base {
					px, py := toScreen.Apply(p.X, p.Y)
					if dist(px, py, float64(e.X), float64(e.Y)) < 15 {
						pointStart.X, pointStart.Y = p.X, p.Y
						new = i
						break
					}
				}
				dragging = true
				dragPoint = new
				selectPoint(new)
			} else if e.Button == 1 && e.State == sdl.RELEASED {
				dragging = false
				fracRect = frac.AdjustedBounds(fracPortRect, fracPortScale)
				toScreen, fromScreen = NewAffinesBetween(fracRect, fracPortRect)
			}
		case *sdl.MouseMotionEvent:
			if dragging {
				e.X -= fracPort.X
				e.Y -= fracPort.Y
				// don't allow dragging last point
				if dragPoint >= 0 && dragPoint < len(frac.Base)-1 {
					newX, newY := fromScreen.Apply(float64(e.X), float64(e.Y))
					frac.Base[dragPoint].X = newX - mouseStart.X + pointStart.X
					frac.Base[dragPoint].Y = newY - mouseStart.Y + pointStart.Y
					frac.Changed()
				}
			}
		}
	}
	*/

	for {
	}

	return 0
}

func run() {
	var err error

	f, err := os.Create("pdata")
	if err != nil {
		log.Fatal(err)
	}
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	// fracPort := sdl.Rect{200, 0, 1000, 800}
	// fullPort := sdl.Rect{0, 0, 1200, 800}
	// dataPort := sdl.Rect{0, 0, 200, 800}
	fracPortRect := Rect{C0: Coord{5, 5}, C1: Coord{995, 795}}
	fracPortScale := int32(0)
	base := []Point{
		Point{0.05, 0.25, 0, 0, 255, 255},
		Point{0.95, -0.25, 0, 0, 255, 255},
		Point{1, 0, 0, 0, 255, 255},
	}
	frac = NewFractal(base, 18)
	frac.H = 330
	for i := 0; i < frac.MaxDepth; i++ {
		if !frac.Render(i) {
			fmt.Printf("oops, render %d failed.\n", i)
		}
	}
	fracRect := frac.AdjustedBounds(fracPortRect, fracPortScale)
	toScreen, fromScreen := NewAffinesBetween(fracRect, fracPortRect)
	_, _ = toScreen, fromScreen

	cfg := pixelgl.WindowConfig{
		Title:  "Pixel Rocks!",
		Bounds: pixel.R(0, 0, 1200, 800),
		VSync:  true,
	}
	win, err := pixelgl.NewWindow(cfg)
	if err != nil {
		panic(err)
	}
	win.SetComposeMethod(pixel.ComposePlus)
	// win.SetSmooth(true)

	imd := imdraw.New(nil)
	fracMatrix := pixel.IM.Scaled(pixel.Vec{},800).Moved(pixel.Vec{200, 400})
	imd.SetMatrix(fracMatrix)

	second := time.Tick(time.Second)
	frames := 0

	for !win.Closed() {
		if win.JustPressed(pixelgl.MouseButtonLeft) {
			click := win.MousePosition()
			leastDist := 999999.0
			pidx := -1
			for i, p := range frac.Base {
				pv := fracMatrix.Project(pixel.Vec{p.X, p.Y})
				dist := math.Hypot(pv.X - click.X, pv.Y - click.Y)
				if dist < 15 && dist < leastDist {
					leastDist = dist
					pidx = i
				}
			}
			selectPoint(pidx)
			fmt.Printf("click at %.0f, %.0f => %.1f px to point %d\n",
				click.X, click.Y, leastDist, pidx)
		}
		win.Clear(pixel.RGBA{0, 0, 0, 255})
		if frac.Depth < frac.MaxDepth-1 {
			frac.Render(frac.Depth + 1)
		}
		imd.Clear()
		for i := 1; i <= frac.Depth; i++ {
			imd.Push(pixel.Vec{})
			points := frac.Points(i)
			r, g, b := rgb(points[0].H, points[0].S, points[0].V)
			imd.Color = pixel.RGBA { float64(r) / 255, float64(g)/255, float64(b)/255, 1}
			for j := 0; j < len(points); j++ {
				p := points[j]
				imd.Push(pixel.Vec{p.X, p.Y})
			}
			imd.Line(1.0/800)
		}
		if selectedPoint >= 0 {
			p := frac.Base[selectedPoint]
			if selectedPoint > 0 {
				imd.Push(pixel.Vec{frac.Base[selectedPoint - 1].X, frac.Base[selectedPoint - 1].Y})
			} else {
				imd.Push(pixel.Vec{})
			}
			r, g, b := rgb(p.H, p.S, p.V)
			imd.Color = pixel.RGBA { float64(r) / 255, float64(g)/255, float64(b)/255, 1}
			imd.Push(pixel.Vec{p.X, p.Y})
			imd.Line(3.0/800)
		}
		imd.Draw(win)
		win.Update()
		frames++
                select {
                case <-second:
                        fmt.Printf("FPS: %d\n", frames)
                        frames = 0
                default:
                }
 
	}
}

func main() {
	pixelgl.Run(run)
}
