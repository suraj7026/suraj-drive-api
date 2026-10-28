# Publication verification — 2026-10-28

Branch: `migration/image-publication`. Production main branch and deploy workflow
unchanged. Publication source: `afcd3e7734f92a5ee965f8a620f2d66c78643f1a`.

Run: https://github.com/suraj7026/suraj-drive-api/actions/runs/40708327342

**Success:** existing Backend CI (isolated PostgreSQL/MinIO, race tests, vet,
formatting, vulnerability scan and builds) and the non-root image check passed.
The reachable-Go-vulnerability scan reported no vulnerabilities. The candidate
was published without changing `latest`:

```text
ghcr.io/suraj7026/drive-api@sha256:2b0e42d83c5dea01bf355e3c4955d0cd617f7bfad77fa9846917bce4e59478b6
```

SSH verification on surajhomeserver:

- Anonymous pull was permitted for this existing package. The first 40-second
  attempt timed out downloading layers; a bounded 180-second retry completed.
- An isolated `--network none`, read-only, capability-dropped container verified
  non-root identity, server/migrate executables and no embedded `.env`. It exited
  successfully. The API process and migration binary were **not executed**.
- The new check container/image are retained. No production image/tag/container
  was replaced or deleted.
- Both cluster nodes Ready; all eight original application containers running;
  Cloudflare, MinIO, Ollama and PostgreSQL services active.
- Existing API `/api/health` returned 200, `/api/ready` still returned 401;
  public Drive login and Portfolio profile returned 200, resume health returned 200.

No production deploy workflow ran. Its latest run remains the failed configuration
validation from 2026-10-27, run 40501540414. Missing production configuration and
reproducible rollback blockers remain as documented in README.md. This published
candidate is not a production deployment or Kubernetes migration.
