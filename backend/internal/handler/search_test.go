package handler

import (
	"net/http/httptest"
	"testing"
)

func TestParseSearchFilters(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/search?q=report&type=pdf&owner=me&shared=yes&starred=no&trash=all&location=00000000-0000-0000-0000-000000000001&modified_after=2026-01-01T00:00:00Z", nil)
	filters, err := parseSearchFilters(request)
	if err != nil {
		t.Fatalf("parse valid search filters: %v", err)
	}
	if filters.Type != "pdf" || filters.Owner != "me" || filters.Shared != "yes" || filters.Starred != "no" || filters.Trash != "all" || filters.ModifiedAfter == nil {
		t.Fatalf("unexpected filters: %+v", filters)
	}
}

func TestParseSearchFiltersRejectsInvalidValues(t *testing.T) {
	for _, rawQuery := range []string{
		"type=executable", "shared=maybe", "starred=maybe", "trash=forever",
		"location=not-a-uuid", "modified_before=yesterday",
	} {
		request := httptest.NewRequest("GET", "/api/search?"+rawQuery, nil)
		if _, err := parseSearchFilters(request); err == nil {
			t.Fatalf("expected %q to fail", rawQuery)
		}
	}
}
