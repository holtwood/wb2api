---
name: Pull request
about: Propose changes to wb2api
title: ""
labels: ""
assignees: ""
---

**What and why**
What this changes and the problem it solves.

**Checklist**
- [ ] Read AGENTS.md (architecture discipline, reference-directory rules)
- [ ] No third_party/ content added; no code from unlicensed references
- [ ] Protocol logic lives in `core/`, not in the plugin shell
- [ ] `go build ./... && go vet ./... && go test ./...` pass
- [ ] New protocol facts reference a `specs/` capture
- [ ] NOTICE.md updated if borrowing from an MIT reference
