package pagination

import (
	"errors"
	"testing"
	"time"
)

func TestCodecRoundTrip(t *testing.T) {
	codec := NewCodec("test-secret", time.Hour)
	want := Position{Group: 1, Name: "report.pdf", ID: 42, Score: 0.75}
	token, err := codec.Encode("drive:list:root", want)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	got, err := codec.Decode(token, "drive:list:root")
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if got != want {
		t.Fatalf("decoded position = %+v, want %+v", got, want)
	}
}

func TestCodecRejectsTamperingAndWrongScope(t *testing.T) {
	codec := NewCodec("test-secret", time.Hour)
	token, err := codec.Encode("drive:list:root", Position{Group: 0, Name: "folder", ID: 7})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}

	for name, candidate := range map[string]string{
		"tampered":    token[:len(token)-1] + "A",
		"wrong scope": token,
	} {
		scope := "drive:list:root"
		if name == "wrong scope" {
			scope = "drive:list:other"
		}
		if _, err := codec.Decode(candidate, scope); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("%s: expected ErrInvalidCursor, got %v", name, err)
		}
	}
}

func TestCodecRejectsExpiredCursor(t *testing.T) {
	codec := NewCodec("test-secret", time.Minute)
	clock := time.Unix(1_700_000_000, 0)
	codec.now = func() time.Time { return clock }
	token, err := codec.Encode("scope", Position{Group: 1, Name: "file", ID: 1})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := codec.Decode(token, "scope"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expected expired cursor to be rejected, got %v", err)
	}
}
