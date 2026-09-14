package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

const maxReportedIssues = 500

type Issue struct {
	Type      string `json:"type"`
	Reference string `json:"reference"`
	Name      string `json:"name,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
}

type Report struct {
	ExpectedObjects int     `json:"expected_objects"`
	ActualObjects   int     `json:"actual_objects"`
	Consistent      int     `json:"consistent_objects"`
	IssueCount      int     `json:"issue_count"`
	Truncated       bool    `json:"truncated"`
	Issues          []Issue `json:"issues"`
}

func Compare(expected []repository.ExpectedBlob, actual []storage.AuditObject) Report {
	report := Report{ExpectedObjects: len(expected), ActualObjects: len(actual), Issues: make([]Issue, 0)}
	actualByLocation := make(map[string]storage.AuditObject, len(actual))
	for _, object := range actual {
		actualByLocation[location(object.Bucket, object.Key)] = object
	}
	for _, blob := range expected {
		key := location(blob.Bucket, blob.Key)
		object, found := actualByLocation[key]
		if !found {
			if blob.Required && !blob.CleanupQueued {
				report.add(Issue{Type: "missing_blob", Reference: blob.VersionID, Name: blob.Name})
			}
			continue
		}
		delete(actualByLocation, key)
		if blob.CleanupQueued {
			continue
		}
		mismatched := false
		if blob.SizeBytes != object.SizeBytes {
			report.add(Issue{Type: "size_mismatch", Reference: blob.VersionID, Name: blob.Name, Expected: formatInt(blob.SizeBytes), Actual: formatInt(object.SizeBytes)})
			mismatched = true
		}
		if blob.ETag != "" && !strings.EqualFold(strings.Trim(blob.ETag, "\""), strings.Trim(object.ETag, "\"")) {
			report.add(Issue{Type: "etag_mismatch", Reference: blob.VersionID, Name: blob.Name, Expected: blob.ETag, Actual: object.ETag})
			mismatched = true
		}
		if !mismatched {
			report.Consistent++
		}
	}
	for key := range actualByLocation {
		digest := sha256.Sum256([]byte(key))
		report.add(Issue{Type: "orphan_blob", Reference: "blob-" + hex.EncodeToString(digest[:8])})
	}
	return report
}

func (r *Report) add(issue Issue) {
	r.IssueCount++
	if len(r.Issues) < maxReportedIssues {
		r.Issues = append(r.Issues, issue)
	} else {
		r.Truncated = true
	}
}

func location(bucket, key string) string { return bucket + "\x00" + key }

func formatInt(value int64) string { return strconv.FormatInt(value, 10) }
