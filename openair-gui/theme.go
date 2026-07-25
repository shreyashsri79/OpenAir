package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Paper-and-ink palette matching the OpenAir mock: cream background,
// deep navy accents, warm beige buttons.
var (
	colCream = color.NRGBA{R: 0xF7, G: 0xF2, B: 0xE3, A: 0xFF}
	colPaper = color.NRGBA{R: 0xFD, G: 0xFB, B: 0xF3, A: 0xFF}
	colNavy  = color.NRGBA{R: 0x1E, G: 0x4E, B: 0x6B, A: 0xFF}
	colInk   = color.NRGBA{R: 0x18, G: 0x16, B: 0x10, A: 0xFF}
	colBeige = color.NRGBA{R: 0xF2, G: 0xEB, B: 0xD9, A: 0xFF}
	colFaint = color.NRGBA{R: 0x6B, G: 0x66, B: 0x58, A: 0xFF}
)

type openairTheme struct{}

func (openairTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return colCream
	case theme.ColorNameForeground:
		return colInk
	case theme.ColorNamePrimary, theme.ColorNameHyperlink, theme.ColorNameFocus:
		return colNavy
	case theme.ColorNameButton:
		return colBeige
	case theme.ColorNameInputBackground:
		return colPaper
	case theme.ColorNameForegroundOnPrimary:
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	case theme.ColorNamePlaceHolder, theme.ColorNameDisabled:
		return colFaint
	case theme.ColorNameSeparator:
		return color.NRGBA{R: 0x1E, G: 0x4E, B: 0x6B, A: 0x30}
	case theme.ColorNameInputBorder:
		return color.NRGBA{R: 0x18, G: 0x16, B: 0x10, A: 0x60}
	case theme.ColorNameShadow:
		return color.NRGBA{A: 0x18}
	}
	return theme.DefaultTheme().Color(n, theme.VariantLight)
}

func (openairTheme) Font(s fyne.TextStyle) fyne.Resource   { return theme.DefaultTheme().Font(s) }
func (openairTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }

func (openairTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 10
	}
	return theme.DefaultTheme().Size(n)
}
