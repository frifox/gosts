package main

import (
	"slices"
	"testing"
)

// TestModelOptions: the defaults are good; a bad choice is caught; the
// options become apple-obj-cap's flags (the custom ones only with custom).
func TestModelOptions(t *testing.T) {
	o := defaultModelOptions()
	if err := o.check(); err != nil {
		t.Fatal(err)
	}
	a := o.args()
	if !slices.Contains(a, "full") || slices.Contains(a, "--max-polygons") || slices.Contains(a, "--no-masking") {
		t.Errorf("defaults: %v", a)
	}
	o.Detail, o.Masking, o.Gravity, o.TextureFormat = "custom", false, false, "jpeg"
	a = o.args()
	for _, want := range []string{"--no-masking", "--no-gravity", "--max-polygons", "--texture-maps", "--jpeg-quality"} {
		if !slices.Contains(a, want) {
			t.Errorf("custom: no %s in %v", want, a)
		}
	}
	for _, bad := range []func(*modelOptions){
		func(o *modelOptions) { o.Detail = "huge" },
		func(o *modelOptions) { o.Format = "stl" },
		func(o *modelOptions) { o.Detail, o.MaxPolygons = "custom", 0 },
		func(o *modelOptions) { o.Detail, o.TextureMaps = "custom", nil },
		func(o *modelOptions) { o.Detail, o.TextureMaps = "custom", []string{"shine"} },
	} {
		o := defaultModelOptions()
		bad(&o)
		if o.check() == nil {
			t.Errorf("%+v passed", o)
		}
	}
}
