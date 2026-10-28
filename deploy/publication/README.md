# Drive API candidate publication

This branch adds publication only. It does not change `.github/workflows/deploy.yml`,
Compose, production configuration, the database, or Kubernetes resources. Publication
runs on the dedicated `migration/image-publication` branch so the existing main-push
production deployment is not triggered. Do not merge to main without separately
addressing that deployment trigger.

The workflow reuses the complete existing Backend CI: isolated PostgreSQL and MinIO,
race tests, formatting, vet, vulnerability checks, binary/container builds. Only after
that succeeds does it build and publish a candidate image with a commit tag and
recorded digest. It never updates `latest`. The isolated image check verifies the
non-root user, required binaries and absence of an embedded `.env`; it does not
pretend to be a full production runtime test.

## Deployment remains blocked

The deployed backend and this source revision differ significantly. The current
release needs database/SMTP configuration and a separately reviewed schema rollout.
Missing configuration at audit: DATABASE_URL, DATABASE_MIGRATION_URL, SMTP_ADDRESS,
SMTP_USERNAME, SMTP_PASSWORD, SMTP_FROM_ADDRESS. The running image is not available
through `docker image inspect`, so its reproducible rollback artifact is unresolved.
The existing `/api/ready` returns 401 while `/api/health` returns 200.

No migrations or background workers are started against production by this workflow.
No production credentials are supplied. Persistent GHCR pull credentials, pod-level
network tests, ClamAV storage/capacity, notifications, and test-account file operations
must be validated before deployment. Never use the operator cluster-admin kubeconfig
in CI. Do not combine the application/schema release with a Kubernetes move.

## Inspect runs

```bash
gh run list --repo suraj7026/suraj-drive-api --branch migration/image-publication
gh run watch RUN_ID --repo suraj7026/suraj-drive-api --exit-status
```

The workflow prints `PUBLISHED_IMAGE=...@sha256:...` only after successful publication.
Use that digest for later review; a successful build is not a migration or cutover.
