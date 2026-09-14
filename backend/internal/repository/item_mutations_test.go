package repository

import "testing"

func TestValidFolderColor(t *testing.T) {
	for _, color := range []string{"", "red", "orange", "yellow", "green", "blue", "purple", "gray"} {
		if !validFolderColor(color) {
			t.Fatalf("expected %q to be valid", color)
		}
	}
	for _, color := range []string{"pink", "#ffffff", "blue; DROP TABLE"} {
		if validFolderColor(color) {
			t.Fatalf("expected %q to be invalid", color)
		}
	}
}
