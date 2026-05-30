## Summary

<!-- What does this PR do, and why? -->

## Related issues

<!-- e.g. Fixes #123 -->

## PR checklist

- [ ] `make -C cshim` builds cleanly.
- [ ] `go vet ./...` is clean.
- [ ] `gofmt -l .` is empty.
- [ ] User-mode tests pass (`go test -run "TestSessionCreateClose|TestAttachDetach|TestModules" .`).
- [ ] Documentation (`README.md`, `CLAUDE.md`, MCP tool docs) is updated to match new behaviour.
- [ ] No `dbgeng.dll` or other Windows SDK redistributables were committed.
- [ ] Commit messages include a `Co-authored-by:` trailer if AI assistance was used (see [CONTRIBUTING.md](../CONTRIBUTING.md)).

## Notes for reviewers

<!-- Anything reviewers should know: tricky edge cases, follow-ups deferred, etc. -->
