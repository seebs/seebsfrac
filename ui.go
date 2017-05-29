package main

import (
	"fmt"
	"os"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/sdl_ttf"
)

var font *ttf.Font
var renderer *sdl.Renderer

type Glyph struct {
	surface *sdl.Surface
	texture *sdl.Texture
	w, h int32
}

type Field struct {
	Glyph
	fmt string
	value float64
	x, y int32
	r, g, b uint8
}

var foo Field

var characters [128]*Glyph

func NewGlyph(label string) *Glyph {
	var err error
	var gl Glyph
	if gl.surface, err = font.RenderUTF8_Blended(label, sdl.Color{255, 255, 255, 255}); err != nil {
		fmt.Fprintf(os.Stderr, "blended text error: %s\n", err)
		return nil
	}
	if gl.texture, err = renderer.CreateTextureFromSurface(gl.surface); err != nil {
		fmt.Fprintf(os.Stderr, "texture creation error: %s\n", err)
		return nil
	}
	_, _, gl.w, gl.h, err = gl.texture.Query()
	return &gl
}

func (gl Glyph) Draw(x, y int32, r, g, b uint8) int32 {
	gl.texture.SetColorMod(r, g, b)
	renderer.Copy(gl.texture, nil, &sdl.Rect{x, y, gl.w, gl.h})
	return gl.w
}

func NewField(label string, fmt string, r, g, b uint8) *Field {
	var f Field
	f.fmt = fmt
	f.r, f.g, f.b = r, g, b
	glyph := NewGlyph(label)
	if glyph != nil {
		f.Glyph = *glyph
	}
	f.texture.SetColorMod(f.r, f.g, f.b)
	foo = f
	return &f
}

func (g Glyph) Close() {
	g.texture.Destroy()
	g.surface.Free()
}

func (f Field) Close() {
	f.Glyph.Close()
}

func (f *Field) SetValue(v float64) {
	f.value = v
}

func (f Field) DrawValue() {
	digits := fmt.Sprintf(f.fmt, f.value)
	DrawString(f.x + f.w + 5, f.y, digits, f.r, f.g, f.b)
}

func (f Field) DrawValueColor(r, g, b uint8) {
	digits := fmt.Sprintf(f.fmt, f.value)
	DrawString(f.x + f.w + 5, f.y, digits, r, g, b)
}

func (f Field) Move(x, y int32) {
	f.x, f.y = x, y
}

func (f Field) Draw() {
	f.DrawLabel()
	f.DrawValue()
}

func (f Field) DrawLabel() {
	f.Glyph.Draw(f.x, f.y, f.r, f.g, f.b)
}

func (f Field) DrawLabelColor(r, g, b uint8) {
	f.Glyph.Draw(f.x, f.y, r, g, b)
}

func DrawString(x, y int32, s string, r, g, b uint8) {
	for _, c := range s {
		var dx int32
		i := int(c)
		if i >= 0 && i < len(characters) && characters[i] != nil {
			dx = characters[c].Draw(x, y, r, g, b)
			x += dx
		}
	}
}

func UIInit(r *sdl.Renderer) {
	var err error
	renderer = r

	if err = ttf.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize TTF: %s\n", err)
	}

	if font, err = ttf.OpenFont("Go-Mono.ttf", 16); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open font: %s\n", err)
	}

	for i := 32; i < len(characters); i++ {
		characters[i] = NewGlyph(fmt.Sprintf("%c", i))
	}
}

func UIClose() {
	for i := 32; i < len(characters); i++ {
		if characters[i] != nil {
			characters[i].Close()
			characters[i] = nil
		}
	}
	font.Close()
}
