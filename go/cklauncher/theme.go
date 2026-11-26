package cklauncher

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// kioskTheme provides a clean, restrained dark desktop palette and compact spacing.
type kioskTheme struct {
	base fyne.Theme
}

func newKioskTheme() fyne.Theme {
	return &kioskTheme{base: theme.DefaultTheme()}
}

func (t *kioskTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 16, G: 19, B: 26, A: 255}
	case theme.ColorNameOverlayBackground, theme.ColorNameMenuBackground:
		return color.NRGBA{R: 22, G: 26, B: 35, A: 255}
	case theme.ColorNameButton:
		return color.NRGBA{R: 30, G: 36, B: 48, A: 255}
	case theme.ColorNameInputBackground:
		return color.NRGBA{R: 24, G: 29, B: 40, A: 255}
	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return color.NRGBA{R: 42, G: 49, B: 64, A: 255}
	case theme.ColorNameHeaderBackground:
		return color.NRGBA{R: 22, G: 27, B: 37, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 56, G: 189, B: 248, A: 255}
	case theme.ColorNameFocus:
		return color.NRGBA{R: 56, G: 189, B: 248, A: 140}
	case theme.ColorNameHover:
		return color.NRGBA{R: 255, G: 255, B: 255, A: 14}
	case theme.ColorNameSelection:
		return color.NRGBA{R: 56, G: 189, B: 248, A: 48}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 232, G: 237, B: 245, A: 255}
	case theme.ColorNamePlaceHolder:
		return color.NRGBA{R: 120, G: 132, B: 152, A: 255}
	case theme.ColorNameDisabled:
		return color.NRGBA{R: 92, G: 102, B: 120, A: 255}
	case theme.ColorNameError:
		return color.NRGBA{R: 239, G: 68, B: 68, A: 255}
	case theme.ColorNameSuccess:
		return color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	case theme.ColorNameWarning:
		return color.NRGBA{R: 245, G: 158, B: 11, A: 255}
	}
	return t.base.Color(name, theme.VariantDark)
}

func (t *kioskTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *kioskTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *kioskTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameText:
		return 13
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameHeadingText:
		return 18
	case theme.SizeNameInputRadius:
		return 6
	case theme.SizeNameSelectionRadius:
		return 6
	}
	return t.base.Size(name)
}
