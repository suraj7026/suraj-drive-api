package repository

import "testing"

func TestValidPreferenceSortKey(t *testing.T) {
	valid := []string{"default", "name-asc", "name-desc", "date-newest", "date-oldest", "size-largest", "size-smallest"}
	for _, value := range valid {
		if !validPreferenceSortKey(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}
	for _, value := range []string{"", "owner", "name", "DROP TABLE"} {
		if validPreferenceSortKey(value) {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
}
