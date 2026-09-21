package server

import "testing"

func TestRetagImage(t *testing.T) {
	cases := []struct {
		image, tag, want string
	}{
		{"ghcr.io/therealchickenlegs/whatsnewdock:0.1.1", "v0.1.2", "ghcr.io/therealchickenlegs/whatsnewdock:v0.1.2"},
		{"ghcr.io/therealchickenlegs/whatsnewdock", "v0.1.2", "ghcr.io/therealchickenlegs/whatsnewdock:v0.1.2"},
		{"whatsnewdock:latest", "0.1.2", "whatsnewdock:0.1.2"},
		{"localhost:5000/wnd:0.1.1", "0.1.2", "localhost:5000/wnd:0.1.2"},
		{"localhost:5000/wnd", "0.1.2", "localhost:5000/wnd:0.1.2"},
		{"wnd@sha256:abc", "0.1.2", "wnd:0.1.2"},
		{"wnd:0.1.1@sha256:abc", "0.1.2", "wnd:0.1.2"},
		{"", "0.1.2", ""},
		{"wnd:0.1.1", "", ""},
	}
	for _, c := range cases {
		if got := retagImage(c.image, c.tag); got != c.want {
			t.Errorf("retagImage(%q, %q) = %q, want %q", c.image, c.tag, got, c.want)
		}
	}
}
