package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Model a large source through S3 metadata; no multi-gigabyte fixture is needed
// to check range splitting, ETag fencing, and cleanup of failed copy attempts.
func TestLargeSealingUsesConditionalMultipartCopy(t *testing.T) {
	for _, size := range []int64{5<<30 + 17, 100<<30 + 17} {
		for _, failPart := range []int{0, 2} {
			t.Run(fmt.Sprintf("size_%d_failed_part_%d", size, failPart), func(t *testing.T) {
				var ranges []string
				var completed, aborted bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					query := r.URL.Query()
					switch {
					case r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/source"):
						w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
						w.Header().Set("ETag", `"source-etag"`)
						w.Header().Set("Last-Modified", "Tue, 27 Oct 2026 00:00:00 GMT")
						w.Header().Set("Content-Type", "application/octet-stream")
						w.WriteHeader(http.StatusOK)
					case r.Method == http.MethodHead:
						w.WriteHeader(http.StatusNotFound)
					case r.Method == http.MethodPost && query.Has("uploads"):
						fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>drive-test</Bucket><Key>destination</Key><UploadId>copy-id</UploadId></InitiateMultipartUploadResult>`)
					case r.Method == http.MethodPut && query.Get("uploadId") == "copy-id":
						if got := r.Header.Get("X-Amz-Copy-Source-If-Match"); got != `"source-etag"` {
							t.Errorf("unfenced range: %q", got)
						}
						ranges = append(ranges, r.Header.Get("X-Amz-Copy-Source-Range"))
						part, _ := strconv.Atoi(query.Get("partNumber"))
						if part == failPart {
							w.WriteHeader(http.StatusPreconditionFailed)
							fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code><Message>source changed</Message></Error>`)
							return
						}
						fmt.Fprintf(w, `<CopyPartResult><ETag>"part-%d"</ETag><LastModified>2026-10-27T00:00:00Z</LastModified></CopyPartResult>`, part)
					case r.Method == http.MethodPost && query.Get("uploadId") == "copy-id":
						completed = true
						fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>drive-test</Bucket><Key>destination</Key><ETag>"sealed"</ETag></CompleteMultipartUploadResult>`)
					case r.Method == http.MethodDelete && query.Get("uploadId") == "copy-id":
						aborted = true
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected S3 request: %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer server.Close()
				client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("test", "test-secret", ""), Region: "us-east-1"})
				if err != nil {
					t.Fatal(err)
				}
				store := &MinIOClient{client: client}
				err = store.CopyObjectBetweenBucketsIfMatchWithContentType(context.Background(), "drive-test", "source", "drive-test", "destination", "source-etag", "video/mp4")
				if failPart > 0 {
					if err == nil || completed || !aborted {
						t.Fatalf("failed copy was published or leaked: err=%v complete=%v aborted=%v", err, completed, aborted)
					}
				} else {
					if err != nil || !completed || aborted {
						t.Fatalf("copy: err=%v complete=%v aborted=%v", err, completed, aborted)
					}
					if len(ranges) != int(size/(1<<30))+1 || ranges[0] != "bytes=0-1073741823" || ranges[len(ranges)-1] != fmt.Sprintf("bytes=%d-%d", size-17, size-1) {
						t.Fatalf("incorrect copy ranges: %v", ranges)
					}
				}
			})
		}
	}

}
