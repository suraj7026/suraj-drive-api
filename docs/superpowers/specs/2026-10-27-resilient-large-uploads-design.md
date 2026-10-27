# Resilient batch and large-file uploads

Approved scope: prevent a batch of 107 files from stopping when three requests or verification jobs stall; preserve uploaded multipart chunks and make processing state visible.

## Implementation

- Frontend: one bounded scheduler per upload view, shared across file selections and retries. Three file transfers; at most six files or 64 GiB admitted to the processing pipeline (one larger file may run alone). Transfer slots release after the backend accepts completion; verification has its own lifecycle. Every network request has a deadline, chunk uploads have a 90-second no-progress watchdog, and transient part failures retry with fresh URLs and backoff. Failed files do not block later work. Persist transfer metadata; browser reload still requires reselecting local files.
- Backend: multipart server-side sealing preserves ETag conditions and immutable attempt keys. Processing deadlines scale with file size. Short job leases renew while workers are alive; stale workers cannot publish. Once completion is accepted, the transfer reservation deadline no longer invalidates processing. Scanner errors remain errors, never clean verdicts. New reservations retain partial uploads for seven days, with a 64-MiB starting part size; existing part sizes remain valid. Bulk part synchronization avoids serial SQL round trips during large resumes.
- UI: distinguish preparation, transfer, verification and scanning; expose retries and per-file failures, refresh completed files incrementally.

## Validation

Exercise a 107-file batch with three stalled requests, isolated failures, delayed verification, cancellation, fresh-URL chunk retries and processing backpressure. Verify expired upload reservations cannot invalidate accepted completion, job lease renewal is fenced, and multipart copy uses size-appropriate operations. Run repository CI checks. A synthetic large-file test is not proof of a real hundreds-of-GB transfer; document that limitation and the deployed scanner's independent size limits.

## Delivery

Two linked PRs against the existing release-gate branches, keeping unrelated release work out of each diff. No production deployment or security-policy bypass is included.

## Verification results

- Chromium: 107 small files completed with three dropped storage connections after the first 12 transfers; all three retried and no page errors occurred. Verification polling was accelerated in the test harness.
- Frontend: 17 unit regressions, lint, TypeScript and production build passed.
- Backend: race tests passed with an isolated PostgreSQL instance and app-role connection; vet and binary builds passed. S3 wire tests model 5-GiB and 100-GiB objects without transferring those bytes.
- Real MinIO integration is covered by CI; the local Docker daemon was unavailable. Scanner engine limits and a real hundreds-of-GB transfer remain deployment acceptance checks.
