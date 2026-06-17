# Remote Version Test Report - 2026-06-17

This report intentionally omits server addresses, SSH details, private key paths, API keys, and tokens.

## Test Target

- Date: 2026-06-17 Asia/Shanghai
- Operator: Codex
- Candidate git commit: `921820c`
- Functional compatibility commit: `c00c90d`
- Published Docker image tested: not yet published for this candidate
- Runtime used for remote test: current Linux amd64 binary mounted into an isolated test container
- Existing rollback baseline: `czppwa/emby-migrator:sha-8c42aed`

## Local Verification

| Check | Result |
| --- | --- |
| `go test -buildvcs=false ./internal/web ./internal/exporter ./internal/emby` | Passed |
| `go test -buildvcs=false ./...` | Passed |
| `node --check web/assets/app.js` | Passed |
| `go build -buildvcs=false ./cmd/server` | Passed |
| `git diff --check` | Passed |

## Remote Environment

| Component | Version / Mode | Result |
| --- | --- | --- |
| Emby 4.8 test target | `4.8.11.0` | Available |
| Emby 4.9 test target | `4.9.5.0` | Available |
| Test libraries | `日韩剧集`, `日韩电影` | Available on both versions |
| Test export package | `20260616-105725-b4c5d5c2c9e1-日韩剧集等2库` | Available |
| Migrator test instance | isolated host-network container on a non-production port | Healthy |

Note: the remote Docker daemon could not build the image directly because the configured registry mirror returned missing content for base images. The candidate binary was cross-built locally and mounted into an isolated container for runtime validation. Publishing still needs a Docker Hub/GitHub Actions build confirmation.

## Import Verification

| Scenario | Job | Status | Items | Metadata | Unmatched | Ambiguous | Errors | Media Images | Person Avatars | Compatibility |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- | --- | --- |
| package -> Emby 4.9.5 precheck | `1781679152549901651` | done | 274 | dry-run | 0 | 0 | 0 | dry-run | dry-run | `emby-4.9-strict` |
| package -> Emby 4.9.5 import | `1781679173057906273` | done | 274 | 274 | 0 | 0 | 0 | 363 success / 0 failed | 830 success / 0 failed | `emby-4.9-strict` |
| package -> Emby 4.8.11 precheck | `1781679266859982869` | done | 274 | dry-run | 0 | 0 | 0 | dry-run | dry-run | `emby-4.8-classic` |
| package -> Emby 4.8.11 import | `1781679299426115001` | done | 274 | 274 | 0 | 0 | 0 | 363 success / 0 failed | 830 success / 0 failed | `emby-4.8-classic` |
| package -> existing Emby 4.8.11 import | `1781679326348088154` | done | 274 | 274 | 0 | 0 | 0 | 363 success / 0 failed | 830 success / 0 failed | `emby-4.8-classic` |

## Export Verification

| Scenario | Job | Status | Libraries | Items | Media Images | People | Person Avatars | Errors | Duration |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Emby 4.9.5 export | `1781679401838303007` | done | 2 | 274 | 447 | 962 | 830 | 0 | 1m14s |
| Emby 4.8.11 export | `1781679493609145647` | done | 2 | 274 | 447 | 962 | 830 | 0 | 55s |

## Import Verification For Newly Exported Packages

| Scenario | Job | Status | Items | Metadata | Unmatched | Ambiguous | Errors | Media Images | Person Avatars | Compatibility |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- | --- | --- |
| 4.9 export -> Emby 4.8 import, first run | `1781679668791866236` | done | 274 | 272 | 0 | 2 | 0 | 445 success / 0 failed | 830 success / 0 failed | `emby-4.8-classic` |
| 4.9 export -> Emby 4.8 import, after episode path-series fix | `1781680235379441373` | done | 274 | 274 | 0 | 0 | 0 | 447 success / 0 failed | 830 success / 0 failed | `emby-4.8-classic` |
| 4.8 export -> Emby 4.9 import | `1781680262130625606` | done | 274 | 274 | 0 | 0 | 0 | 447 success / 0 failed | 830 success / 0 failed | `emby-4.9-strict` |
| 4.9 export -> Emby 4.9 precheck | `1781680296543676804` | done | 274 | dry-run | 0 | 0 | 0 | dry-run | dry-run | `emby-4.9-strict` |
| 4.8 export -> Emby 4.8 precheck | `1781680296541953741` | done | 274 | dry-run | 0 | 0 | 0 | dry-run | dry-run | `emby-4.8-classic` |

## Result

- Emby 4.8.11 and 4.9.5 import paths passed with full metadata, media images, and person avatar writes.
- Emby 4.8.11 and 4.9.5 export paths passed with matching counts and zero export errors.
- Newly exported 4.8 and 4.9 packages were validated back against both major versions through cross-import and same-version precheck.
- The 4.9 strict compatibility path was exercised through the automated smoke matrix and remote 4.9 import.
- An initial 4.9-export-to-4.8 import exposed two same-title episode ambiguities. The matcher was fixed to use episode path-series candidates during episode matching and name-search fallback. The retest completed with zero ambiguous items.
- No unmatched or errored media items were observed in the final verified run.

## Remaining Release Gate

- Push the candidate commits only when ready to publish.
- Confirm GitHub Actions builds and pushes the Docker image.
- Confirm Docker Hub has the new `latest` and `sha-*` tags.
- Update the accepted rollback baseline only after the published image is verified.
