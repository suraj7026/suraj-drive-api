package repository

import (
	"reflect"
	"testing"
)

func TestMentionedEmails(t *testing.T) {
	got := mentionedEmails("Please ask @Person.Example+drive@example.com and @person.example+drive@example.com, not @invalid.")
	want := []string{"person.example+drive@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mentionedEmails() = %#v, want %#v", got, want)
	}
}

func TestValidateCommentBody(t *testing.T) {
	if _, err := validateCommentBody("   "); err == nil {
		t.Fatal("expected blank comment to fail")
	}
	if body, err := validateCommentBody("  useful comment  "); err != nil || body != "useful comment" {
		t.Fatalf("unexpected validated comment: %q err=%v", body, err)
	}
}
