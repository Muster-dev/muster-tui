package theme

import "testing"

func TestSetSurfacesRaisesFromTheBackground(t *testing.T) {
	s0, e0 := Surface, Element
	defer func() { Surface, Element = s0, e0 }()

	setSurfaces("#282c34") // Ghostty's default background
	if Surface.Dark <= "#282c34" || Element.Dark <= Surface.Dark {
		t.Fatalf("dark: surface %s and element %s should step up from #282c34", Surface.Dark, Element.Dark)
	}
	setSurfaces("#ffffff")
	if Surface.Light >= "#ffffff" || Element.Light >= Surface.Light {
		t.Fatalf("light: surface %s and element %s should step down from white", Surface.Light, Element.Light)
	}
	before := Surface
	setSurfaces("nope")
	if Surface != before {
		t.Fatal("a bad colour changed the surface")
	}
}
