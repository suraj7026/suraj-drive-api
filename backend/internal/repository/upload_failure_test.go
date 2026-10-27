package repository

import (
	"strings"
	"testing"
)

func TestUploadFailureMessageDoesNotExposeInfrastructureDetails(t *testing.T) {
	if got := uploadFailureMessage("verifying", "internal failure"); got != "" {
		t.Fatal(got)
	}
	detail := "ClamAV rejected stream: INSTREAM size limit exceeded https://private.invalid/?secret=credential"
	got := uploadFailureMessage("failed", detail)
	if !strings.Contains(got, "supported size") || strings.Contains(got, "private.invalid") || strings.Contains(got, "credential") {
		t.Fatal(got)
	}
	if got := uploadFailureMessage("failed", "ERROR: query failed with database password"); strings.Contains(got, "password") {
		t.Fatal(got)
	}
}
