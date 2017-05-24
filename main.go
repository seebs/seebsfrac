package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
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
	x, y float32
	flags int
	h, s, v uint16 // 0-360, 0-255, 0-255
}

const (
	WindowTitle = "Frac"
	WindowWidth = 1200
	WindowHeight = 800
	FrameRate = 30
)

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
	frac := []Point{
	  Point{ 1.0/3, 0, 0, 0, 255, 255 },
	  Point{ .5, .5, 0, 80, 255, 255 },
	  Point{ 2.0/3, 0, 0, 160, 255, 255 },
	  Point{ 1, 0, 0, 240, 255, 255 },
	}
	offset := uint16(0)

	sdl.Do(func() {
		window, err = sdl.CreateWindow(WindowTitle, sdl.WINDOWPOS_UNDEFINED, sdl.WINDOWPOS_UNDEFINED, WindowWidth, WindowHeight, sdl.WINDOW_OPENGL)
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
		offset = (offset + 1) % 360
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
			renderer.FillRect(&sdl.Rect{0, 0, WindowWidth, WindowHeight})
		})

		// Do expensive stuff using goroutines
		wg := sync.WaitGroup{}
		prev := Point{}
		sdl.Do(func() {
			for i := range frac {
				r, g, b := rgb(frac[i].h + offset, frac[i].s, frac[i].v)
				renderer.SetDrawColor(uint8(r), uint8(g), uint8(b), 0xff)
				x0, y0 := (int)(prev.x * 800) + 200, (int)(prev.y * 400) + 400
				prev = frac[i]
				x1, y1 := (int)(frac[i].x * 800) + 200, (int)(frac[i].y * 400) + 400
				renderer.DrawLine(x0, y0, x1, y1)
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
