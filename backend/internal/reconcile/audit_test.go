package reconcile

import (
	"testing"

	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

func TestCompareBlobInventory(t *testing.T) {
	expected := []repository.ExpectedBlob{
		{VersionID: "v1", Name: "good.txt", Bucket: "bucket", Key: "good", SizeBytes: 4, ETag: "etag-1", Required: true},
		{VersionID: "v2", Name: "missing.txt", Bucket: "bucket", Key: "missing", SizeBytes: 2, ETag: "etag-2", Required: true},
		{VersionID: "v3", Name: "uploading.txt", Bucket: "bucket", Key: "uploading", SizeBytes: 8},
		{VersionID: "v4", Name: "cleanup.txt", Bucket: "bucket", Key: "cleanup", SizeBytes: 3, CleanupQueued: true},
		{VersionID: "v5", Name: "changed.txt", Bucket: "bucket", Key: "changed", SizeBytes: 10, ETag: "expected", Required: true},
	}
	actual := []storage.AuditObject{
		{Bucket: "bucket", Key: "good", SizeBytes: 4, ETag: "etag-1"},
		{Bucket: "bucket", Key: "cleanup", SizeBytes: 3, ETag: "cleanup-etag"},
		{Bucket: "bucket", Key: "changed", SizeBytes: 11, ETag: "actual"},
		{Bucket: "bucket", Key: "orphan", SizeBytes: 1, ETag: "orphan-etag"},
	}
	report := Compare(expected, actual)
	if report.ExpectedObjects != 5 || report.ActualObjects != 4 || report.Consistent != 1 || report.IssueCount != 4 || report.Truncated {
		t.Fatalf("unexpected audit summary: %+v", report)
	}
	wantTypes := map[string]bool{"missing_blob": false, "size_mismatch": false, "etag_mismatch": false, "orphan_blob": false}
	for _, issue := range report.Issues {
		if _, exists := wantTypes[issue.Type]; !exists {
			t.Fatalf("unexpected issue type: %+v", issue)
		}
		wantTypes[issue.Type] = true
		if issue.Type == "orphan_blob" && issue.Reference == "" {
			t.Fatal("orphan reference must be opaque and non-empty")
		}
	}
	for issueType, found := range wantTypes {
		if !found {
			t.Fatalf("expected issue %s was not reported", issueType)
		}
	}
}

func TestCompareCapsDetailedIssues(t *testing.T) {
	actual := make([]storage.AuditObject, maxReportedIssues+3)
	for index := range actual {
		actual[index] = storage.AuditObject{Bucket: "bucket", Key: "orphan-" + formatInt(int64(index))}
	}
	report := Compare(nil, actual)
	if report.IssueCount != maxReportedIssues+3 || len(report.Issues) != maxReportedIssues || !report.Truncated {
		t.Fatalf("issue cap was not applied: %+v", report)
	}
}
