## Summary

Describe what changed and why.

## Related Issue

Required: link the tracked issue.

Fixes #

## Type of Change

- [ ] Bug fix
- [ ] New feature
- [ ] Refactor
- [ ] Breaking change
- [ ] Configuration schema change
- [ ] Database migration
- [ ] Dashboard/UI change
- [ ] Security/auth/rate-limit change
- [ ] Packaging/systemd/install change
- [ ] Docs/spec update

## Verification

Required for all PRs:

- [ ] go test ./...
- [ ] go build -o bin/gateway ./cmd/gateway/

If applicable:

- [ ] Built packages with ./scripts/build-packages.sh
- [ ] Manually verified dashboard behavior in browser
- [ ] Verified API behavior with expected status codes and error payloads

## Risk and Impact Checklist

Complete every item that applies to this PR.

- [ ] If auth/security logic changed: validated admin token flow, API key handling, and rate-limit behavior
- [ ] If model routing changed: validated backend selection, health-check fallback, and user model restrictions
- [ ] If config fields changed: updated configs/config.example.yaml and documented defaults/compatibility
- [ ] If DB schema changed: added migration under internal/db/migrations and described upgrade/rollback notes
- [ ] If packaging changed: verified packaging/nfpm.yaml, service unit/scripts, and install paths
- [ ] If docs/spec behavior changed: updated relevant docs/specs files

## Dashboard Evidence (Optional)

If this PR changes dashboard UI/UX, include screenshots or short recordings.

- Before:
- After:

## Rollout Notes

Document any operational steps for deployers.

- Restart required: [ ] Yes [ ] No
- Config change required: [ ] Yes [ ] No
- Data migration required: [ ] Yes [ ] No
- Additional notes:

## Reviewer Notes

Anything reviewers should focus on (edge cases, risky paths, known limitations).
