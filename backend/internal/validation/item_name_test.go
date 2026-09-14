package validation

import (
	"errors"
	"strings"
	"testing"
)

func TestItemName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{name: "Quarterly report.pdf", valid: true},
		{name: "résumé.txt", valid: true},
		{name: ""},
		{name: "   "},
		{name: "."},
		{name: ".."},
		{name: ".keep"},
		{name: ".PREVIEWS"},
		{name: "nested/file.txt"},
		{name: "nested\\file.txt"},
		{name: "line\nbreak.txt"},
		{name: strings.Repeat("x", maxItemNameRunes+1)},
	}

	for _, test := range tests {
		err := ItemName(test.name)
		if test.valid && err != nil {
			t.Errorf("ItemName(%q) returned %v", test.name, err)
		}
		if !test.valid && !errors.Is(err, ErrInvalidItemName) {
			t.Errorf("ItemName(%q) error = %v, want ErrInvalidItemName", test.name, err)
		}
	}
}

func TestItemPath(t *testing.T) {
	for _, valid := range []string{"file.txt", "Projects/report.pdf", "Photos/2026/image.jpg"} {
		if err := ItemPath(valid, false); err != nil {
			t.Errorf("ItemPath(%q) returned %v", valid, err)
		}
	}
	if err := ItemPath("", true); err != nil {
		t.Fatalf("empty optional path returned %v", err)
	}
	for _, invalid := range []string{"", "/absolute.txt", "trailing/", "a//b", "a/../b", "a\\b", ".previews/file.jpg"} {
		if err := ItemPath(invalid, false); !errors.Is(err, ErrInvalidItemName) {
			t.Errorf("ItemPath(%q) error = %v, want ErrInvalidItemName", invalid, err)
		}
	}
}
