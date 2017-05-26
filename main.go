package main

import (
	"fmt"
	"os"
	"sync"
	"math"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/sdl_gfx"
)

// flags
const (
  Invisible = 1 << iota
  Prune
  FlipX
  FlipY
  FixedH
  FixedS
  FixedV
)

type Point struct {
	X, Y float64
	Flags int
	H, S, V uint16 // 0-360, 0-255, 0-255
}

func (p Point) GoString() string {
	return fmt.Sprintf("%.3f, %.3f, 0x%03x, %d/%d/%d", p.X, p.Y, p.Flags, p.H, p.S, p.V)
}

var (
	ZeroPoint = Point { X: 0, Y: 0 }
	UnitLine = []Point { Point { X: 1, Y: 0 } }
)

type Fractal struct {
	dataSize int
	MaxDepth int
	Depth int
	Base []Point
	data []Point
	lines [][]Point
}

func NewFractal(base []Point, max int) *Fractal {
	f := new(Fractal)
	f.Base = base[:]
	f.MaxDepth = max
	f.Depth = 0
	totals := make([]int, f.MaxDepth)
	total := 0
	size := 1
	for i := 0; i < max; i++ {
		total += size
		totals[i] = total
		size *= len(f.Base)
	}
	f.data = make([]Point, total, total)
	// first line is trivial case: it has one point after 0,0, which is 1,0
	f.data[0] = UnitLine[0]
	f.lines = make([][]Point, f.MaxDepth)
	prev := 0
	fmt.Printf("%d points, %d depth, %d total size.\n", len(f.Base), f.MaxDepth, total)
	for i := 0; i < max; i++ {
		fmt.Printf("depth %d: %d to %d\n", i, prev, totals[i])
		f.lines[i] = f.data[prev:totals[i]]
		prev = totals[i]
	}
	return f
}

func (f *Fractal) Points(depth int) []Point {
	if depth > f.Depth || depth < 0 {
		return nil
	}
	if depth == 0 {
		return UnitLine
	}
	return f.lines[depth]
}

func (f *Fractal) Render(depth int) bool {
	var src []Point
	// the 0-depth case is already filled in
	if depth == 0 {
		return true
	}
	if depth > 0 && depth <= f.MaxDepth {
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
	for p := range(src) {
		// fmt.Printf("rendering partial %d [%d:%d]\n", p, offset, offset + l)
		f.Partial(prev, src[p], dest[offset:offset + l])
		prev = src[p]
		offset += l
	}
	if f.Depth < depth {
		f.Depth = depth
	}
	return true
}

func (f *Fractal) Partial(p0 Point, p1 Point, dest []Point) {
	dx, dy := p1.X - p0.X, p1.Y - p0.Y
	scale := math.Sqrt(dx * dx + dy * dy)
	theta := math.Atan2(dy, dx)
	cost := math.Cos(theta)
	sint := math.Sin(theta)
	// x1 x2 x0   x   x'
	// y1 y2 y0 * y = y'
	// 0  0  1    1   1
	x1, y1, x2, y2 := scale * cost, scale * sint, -scale * sint, scale * cost

	for i := 0; i < len(f.Base); i++ {
		p := f.Base[i]
		dest[i].X = x1 * p.X + x2 * p.Y + p0.X
	        dest[i].Y = y1 * p.X + y2 * p.Y + p0.Y
		dest[i].H = (p.H + p1.H) % 360
		dest[i].S = p.S
		dest[i].V = p.V
		// fmt.Printf("... point %d: %v\n", i, dest[i])
	}
}

type wininfo struct {
	Title string
	Width, Height int
}

const (
	FrameRate = 30
)

var MainWinInfo = wininfo { Title: "Frac", Width: 1200, Height: 800 }

var runningMutex sync.Mutex

// r, g, b = rgb(frac[i].h, frac[i].s, frac[i].v)
func rgb(h, s, v uint16) (r, g, b uint16) {
	h = (h + 720) % 360
	q := h/60
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
	r, g, b = r + m, g + m, b + m
	return
}

func run() int {
	var window *sdl.Window
	var renderer *sdl.Renderer
	var err error
	fracPort := sdl.Rect { 200, 0, 1200, 800 }
	// dataPort := sdl.Rect { 0, 0, 200, 800 }
	fullPort := sdl.Rect { 0, 0, 1200, 800 }
	base := []Point{
		Point{ 0.05, 0.25, 0, 0, 255, 255 },
		Point{ 0.95, -0.25, 0, 20, 255, 255 },
		Point{ 1, 0, 0, 30, 255, 255 },
	}
	frac := NewFractal(base, 10)
	for i := 0; i < frac.MaxDepth; i++ {
		if !frac.Render(i) {
			fmt.Printf("oops, render %d failed.\n", i)
		}
	}

	sdl.Do(func() {
		window, err = sdl.CreateWindow(MainWinInfo.Title, sdl.WINDOWPOS_UNDEFINED, sdl.WINDOWPOS_UNDEFINED, MainWinInfo.Width, MainWinInfo.Height, sdl.WINDOW_OPENGL)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create window: %s\n", err)
		return 1
	}
	defer func() {
		sdl.Do(func() {
			window.Destroy()
		})
	}()

	sdl.Do(func() {
		renderer, err = sdl.CreateRenderer(window, -1, sdl.RENDERER_ACCELERATED)
	})
	if err != nil {
		fmt.Fprint(os.Stderr, "Failed to create renderer: %s\n", err)
		return 2
	}
	defer func() {
		sdl.Do(func() {
			renderer.Destroy()
		})
	}()

	sdl.Do(func() {
		renderer.Clear()
	})

	running := true
	for running {
		// offset = (offset + 1) % 360
		sdl.Do(func() {
			for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
				switch event.(type) {
				case *sdl.QuitEvent:
					runningMutex.Lock()
					running = false
					runningMutex.Unlock()
				}
			}

			renderer.Clear()
			renderer.SetDrawColor(0, 0, 0, 0x20)
			renderer.SetViewport(nil)
			renderer.FillRect(&fullPort)
		})

		// Do expensive stuff using goroutines
		wg := sync.WaitGroup{}
		sdl.Do(func() {
			renderer.SetViewport(&fracPort)
			for i := 1; i <= frac.Depth; i++ {
				points := frac.Points(i)
				prev := ZeroPoint
				for j := 0; j < len(points); j++ {
					p := points[j]
					r, g, b := rgb(p.H, p.S, p.V)
					x0, y0 := (int)(prev.X * 800) + 200, (int)(prev.Y * -800) + 400
					prev = p
					x1, y1 := (int)(p.X * 800) + 200, (int)(p.Y * -800) + 400
					gfx.AALineColor(renderer, x0, y0, x1, y1, sdl.Color{uint8(r), uint8(g), uint8(b), 255})
				}
			}
			frac.Base[0].Y += .0001
			frac.Base[1].Y -= .0001
			for i := 0; i < frac.MaxDepth; i++ {
				if !frac.Render(i) {
					fmt.Printf("oops, render %d failed.\n", i)
				}
			}
		})
		wg.Wait()

		sdl.Do(func() {
			renderer.Present()
			sdl.Delay(1000 / FrameRate)
		})
	}

	return 0
}

func main() {
	sdl.Main(func() {
		os.Exit(run())
	})
}
