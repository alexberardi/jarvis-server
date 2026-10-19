package computeruse

import "testing"

func TestToGlobal(t *testing.T) {
	cases := []struct {
		name   string
		g      Geometry
		x, y   int
		gx, gy int
	}{
		{"scale 1", Geometry{LogicalW: 1920, LogicalH: 1080, Scale: 1, ImageW: 1920, ImageH: 1080}, 100, 200, 100, 200},
		{"scale 2 retina", Geometry{LogicalW: 1440, LogicalH: 900, Scale: 2, ImageW: 2880, ImageH: 1800}, 1001, 3, 501, 2},
		{"scale 2 half rounds up", Geometry{LogicalW: 1440, LogicalH: 900, Scale: 2, ImageW: 2880, ImageH: 1800}, 1, 1, 1, 1},
		{"scale 1.25", Geometry{LogicalW: 1536, LogicalH: 864, Scale: 1.25, ImageW: 1920, ImageH: 1080}, 1000, 500, 800, 400},
		{"scale 1.25 rounding", Geometry{LogicalW: 1536, LogicalH: 864, Scale: 1.25, ImageW: 1920, ImageH: 1080}, 3, 2, 2, 2},
		{"downscaled for the model", Geometry{LogicalW: 2560, LogicalH: 1440, Scale: 1, ImageW: 1280, ImageH: 720}, 640, 360, 1280, 720},
		{"non-zero origin", Geometry{OriginX: 100, OriginY: 50, LogicalW: 800, LogicalH: 600, Scale: 1, ImageW: 800, ImageH: 600}, 10, 20, 110, 70},
		{"second monitor right", Geometry{OriginX: 1920, LogicalW: 2560, LogicalH: 1440, Scale: 2, ImageW: 5120, ImageH: 2880, Monitor: "DP-2"}, 100, 100, 1970, 50},
		{"second monitor left", Geometry{OriginX: -1440, OriginY: -200, LogicalW: 1440, LogicalH: 900, Scale: 1, ImageW: 1440, ImageH: 900, Monitor: "HDMI-1"}, 0, 0, -1440, -200},
		{"clamp past the edge", Geometry{OriginX: 1920, LogicalW: 1920, LogicalH: 1080, Scale: 1, ImageW: 1920, ImageH: 1080}, 5000, 5000, 3839, 1079},
		{"clamp negative", Geometry{OriginX: 1920, OriginY: 10, LogicalW: 1920, LogicalH: 1080, Scale: 1, ImageW: 1920, ImageH: 1080}, -5, -1, 1920, 10},
		{"last pixel rounds to the edge", Geometry{LogicalW: 1440, LogicalH: 900, Scale: 2, ImageW: 2880, ImageH: 1800}, 2879, 1799, 1439, 899},
		{"no image size: by scale", Geometry{LogicalW: 1440, LogicalH: 900, Scale: 2}, 1001, 3, 501, 2},
		{"no image size, no scale", Geometry{LogicalW: 1440, LogicalH: 900}, 7, 9, 7, 9},
	}
	for _, c := range cases {
		gx, gy := c.g.ToGlobal(c.x, c.y)
		if gx != c.gx || gy != c.gy {
			t.Errorf("%s: ToGlobal(%d, %d) = (%d, %d), want (%d, %d)", c.name, c.x, c.y, gx, gy, c.gx, c.gy)
		}
	}
}

func TestAdapters(t *testing.T) {
	for _, c := range []struct {
		model, want string
	}{
		{"Qwen3.5-9B", "norm1000"},
		{"qwen3.8-27b-instruct-q4_k_m", "norm1000"},
		{"unsloth/Qwen3.5-VL-7B", "norm1000"},
		{"qwen2.5-vl-7b", "pixels"},
		{"claude-sonnet-5-5", "pixels"},
		{"", "pixels"},
	} {
		if got := AdapterFor(c.model).Name; got != c.want {
			t.Errorf("AdapterFor(%q) = %s, want %s", c.model, got, c.want)
		}
	}

	const w, h = 1280, 720
	for _, c := range []struct {
		adapter    Adapter
		x, y       int
		wantX, wan int
	}{
		{Pixels, 100, 200, 100, 200},
		{Pixels, 2000, -3, w - 1, 0},
		{Norm1000, 0, 0, 0, 0},
		{Norm1000, 500, 500, 640, 360},
		{Norm1000, 999, 999, 1278, 719},
		{Norm1000, 1000, 1000, w - 1, h - 1},
		{Unit, 0, 0, 0, 0},
		{Unit, 1, 1, w - 1, h - 1},
	} {
		x, y := c.adapter.ToImage(c.x, c.y, w, h)
		if x != c.wantX || y != c.wan {
			t.Errorf("%s.ToImage(%d, %d) = (%d, %d), want (%d, %d)", c.adapter.Name, c.x, c.y, x, y, c.wantX, c.wan)
		}
	}

	for _, c := range []struct {
		adapter    Adapter
		x, y       float64
		wantX, wan int
	}{
		{Unit, 0.5, 0.25, 640, 180},
		{Unit, 0.9999, 1.5, 1279, 719},
		{Norm1000, 500.4, 250, 640, 180},
		{Pixels, 99.5, 10.49, 100, 10},
	} {
		x, y := c.adapter.ToImageF(c.x, c.y, w, h)
		if x != c.wantX || y != c.wan {
			t.Errorf("%s.ToImageF(%v, %v) = (%d, %d), want (%d, %d)", c.adapter.Name, c.x, c.y, x, y, c.wantX, c.wan)
		}
	}

	if a, ok := AdapterNamed("unit"); !ok || a.Name != "unit" {
		t.Errorf("AdapterNamed(unit) = %v, %v", a.Name, ok)
	}
	if _, ok := AdapterNamed("polar"); ok {
		t.Error("AdapterNamed(polar) found")
	}
}
