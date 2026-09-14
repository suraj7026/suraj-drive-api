package notification

import (
	"encoding/json"
	"net/mail"
	"strings"
	"testing"

	"surajdrive/backend/internal/repository"
)

func TestRenderInvitation(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"item_id": "item-id"})
	message, err := Render(repository.NotificationJob{
		Recipient: "invitee@example.com", Type: "share.invited", Payload: payload,
	}, "https://drive.example.com")
	if err != nil {
		t.Fatalf("render invitation: %v", err)
	}
	if message.To != "invitee@example.com" || !strings.Contains(message.Subject, "invited") || !strings.Contains(message.Text, "https://drive.example.com/login") {
		t.Fatalf("unexpected invitation email: %+v", message)
	}
}

func TestEncodeMessageStripsHeaderNewlines(t *testing.T) {
	from, _ := mail.ParseAddress("Drive <drive@example.com>")
	to, _ := mail.ParseAddress("user@example.com")
	encoded := encodeMessage(from, to, Email{Subject: "Hello\r\nBcc: attacker@example.com", Text: "text", HTML: "<p>text</p>"})
	if strings.Contains(encoded, "\r\nBcc:") {
		t.Fatal("subject allowed header injection")
	}
}
