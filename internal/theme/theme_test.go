package theme

import "testing"

func TestPaletteNames(t *testing.T) {
	names := Names()
	if len(names) != 6 {
		t.Fatalf("names = %v, want six palettes", names)
	}
	if names[0] != "catppuccin-mocha" {
		t.Fatalf("first palette = %q, want catppuccin-mocha", names[0])
	}
}

func TestUnknownPaletteFallsBack(t *testing.T) {
	got := Get("nope")
	want := Get("catppuccin-mocha")
	if got != want {
		t.Fatalf("Palette(nope) = %+v, want the default", got)
	}
	for _, field := range []string{string(got.Accent), string(got.Muted), string(got.Alert), string(got.Mail), string(got.Chat), string(got.Border)} {
		if field == "" {
			t.Fatalf("palette %+v has an empty colour", got)
		}
	}
}

func TestResolveAlias(t *testing.T) {
	if got, ok := Resolve(" Gruvbox "); !ok || got != "gruvbox-dark" {
		t.Fatalf("Resolve(gruvbox) = %q, %v", got, ok)
	}
	if _, ok := Resolve("nope"); ok {
		t.Fatal("Resolve accepted an unknown palette")
	}
}
