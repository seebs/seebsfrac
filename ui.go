package main

import (
	"fmt"
	"os"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/sdl_ttf"
)

type Glyph struct {
	ui      *UI
	surface *sdl.Surface
	texture *sdl.Texture
	w, h    int32
}

type Field struct {
	ui         *UI
	label      Glyph
	fmt        string
	active     bool
	labeltext  string
	value      float64
	x, y       int32
	lr, lg, lb uint8
	vr, vg, vb uint8
}

type Drawable interface {
	Draw()
	Close()
	IsClicked(x, y int32) bool
}

type Button struct {
	ui        *UI
	x, y      int32
	label     Glyph
	labeltext string
	callback  func()
	r, g, b   uint8
}

func (b *Button) Close() {
	b.label.Close()
}

func (b *Button) Draw() {
	b.label.Draw(b.x, b.y, b.r, b.g, b.b)
}

func (u *UI) NewButton(label string, x, y int32, r, g, b uint8, callback func()) *Button {
	var bt Button
	bt.labeltext = label
	bt.ui = u
	bt.x, bt.y = x, y
	bt.r, bt.g, bt.b = r, g, b
	bt.callback = callback
	glyph := u.NewGlyph(label)
	if glyph != nil {
		bt.label = *glyph
	}
	u.fields = append(u.fields, &bt)
	return &bt
}

func (b *Button) IsClicked(x, y int32) bool {
	if x >= b.x && x <= b.x+b.label.w &&
		y >= b.y && y <= b.y+b.label.h {
		b.callback()
		return true
	}
	return false
}

func (f *Field) IsClicked(x, y int32) bool {
	return false
}

type UI struct {
	font       *ttf.Font
	renderer   *sdl.Renderer
	characters [128]*Glyph
	fields     []Drawable
}

func (u UI) NewGlyph(label string) *Glyph {
	var err error
	var gl Glyph
	gl.ui = &u
	if gl.surface, err = u.font.RenderUTF8_Blended(label, sdl.Color{255, 255, 255, 255}); err != nil {
		fmt.Fprintf(os.Stderr, "blended text error: %s\n", err)
		return nil
	}
	if gl.texture, err = u.renderer.CreateTextureFromSurface(gl.surface); err != nil {
		fmt.Fprintf(os.Stderr, "texture creation error: %s\n", err)
		return nil
	}
	_, _, gl.w, gl.h, err = gl.texture.Query()
	return &gl
}

func (gl Glyph) Draw(x, y int32, r, g, b uint8) int32 {
	gl.texture.SetColorMod(r, g, b)
	gl.ui.renderer.Copy(gl.texture, nil, &sdl.Rect{x, y, gl.w, gl.h})
	return gl.w
}

func (u *UI) NewField(label string, x, y int32, fmt string, r, g, b uint8) *Field {
	var f Field
	f.labeltext = label
	f.ui = u
	f.x, f.y = x, y
	f.fmt = fmt
	f.lr, f.lg, f.lb = r, g, b
	f.vr, f.vg, f.vb = r, g, b
	glyph := u.NewGlyph(label)
	if glyph != nil {
		f.label = *glyph
	}
	u.fields = append(u.fields, &f)
	return &f
}

func (g Glyph) Close() {
	g.texture.Destroy()
	g.surface.Free()
}

func (f *Field) Close() {
	f.label.Close()
}

func (f *Field) SetValue(v float64) {
	f.value = v
	f.active = true
}

func (f *Field) SetActive(b bool) {
	f.active = b
}

func (f *Field) SetLabelColor(r, g, b uint8) {
	f.lr, f.lg, f.lb = r, g, b
}

func (f *Field) SetValueColor(r, g, b uint8) {
	f.vr, f.vg, f.vb = r, g, b
}

func (f *Field) DrawValue() {
	digits := fmt.Sprintf(f.fmt, f.value)
	f.ui.DrawString(f.x+f.label.w+5, f.y, digits, f.vr, f.vg, f.vb)
}

func (f *Field) Move(x, y int32) {
	f.x, f.y = x, y
}

func (f *Field) Draw() {
	f.DrawLabel()
	if f.active {
		f.DrawValue()
	}
}

func (u UI) Draw() {
	for _, f := range u.fields {
		f.Draw()
	}
}

func (f Field) DrawLabel() {
	f.label.Draw(f.x, f.y, f.lr, f.lg, f.lb)
}

func (u UI) DrawString(x, y int32, s string, r, g, b uint8) {
	for _, c := range s {
		var dx int32
		i := int(c)
		if i >= 0 && i < len(u.characters) && u.characters[i] != nil {
			dx = u.characters[c].Draw(x, y, r, g, b)
			x += dx
		}
	}
}

func NewUI(r *sdl.Renderer) *UI {
	var u UI
	var err error
	u.renderer = r
	u.fields = make([]Drawable, 0)

	if err = ttf.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize TTF: %s\n", err)
	}

	if u.font, err = ttf.OpenFont("Go-Mono.ttf", 16); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open font: %s\n", err)
	}

	for i := 32; i < len(u.characters); i++ {
		u.characters[i] = u.NewGlyph(fmt.Sprintf("%c", i))
	}
	return &u
}

func (u *UI) IsClicked(x, y int32) bool {
	for _, f := range u.fields {
		if f.IsClicked(x, y) {
			return true
		}
	}
	return false
}

func (u *UI) Close() {
	for i := 32; i < len(u.characters); i++ {
		if u.characters[i] != nil {
			u.characters[i].Close()
			u.characters[i] = nil
		}
	}
	for _, f := range u.fields {
		f.Close()
	}
	u.font.Close()
}
