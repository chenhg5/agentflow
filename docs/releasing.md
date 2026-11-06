# Releasing AgentFlow

Versions follow semver with a `v` prefix. Before 1.0, minor and patch releases may change the workflow schema or CLI contract. The GitHub Releases page is the source of truth.

## Checklist

1. Record the release in `changelogs/<version>.md` and leave `changelogs/unreleased.md` ready for the next release.
2. Set `VERSION := <version>` in `Makefile`.
3. Run `make check`, `make test-e2e`, and `make test-benchmark`. Verify `make build && bin/agentflow version --format json`.
4. Commit the release files:

   ```bash
   git add -A
   git commit -m "release: v0.0.1"
   git tag -a v0.0.1 -m "v0.0.1"
   git push origin HEAD v0.0.1
   ```

5. Pushing the tag runs `.github/workflows/release.yml`: it checks that the tag matches `VERSION` and that `changelogs/<version>.md` exists, runs `make release-all` (which runs `make check` first), and publishes the archives and `checksums.txt` as a GitHub prerelease with the changelog as notes. To build locally instead, run `make release-all` and publish `dist/` with `gh release create <version> dist/*.tar.gz dist/*.zip dist/checksums.txt --title <version> --notes-file changelogs/<version>.md --prerelease`.

6. Watch the Release workflow, then check the release page.

7. Verify the tagged install and binary metadata:

   ```bash
   go install github.com/chenhg5/agentflow/cmd/agentflow@v0.0.1
   agentflow version --format table
   ```

The release binary embeds the tag, commit, and UTC build time through the `Makefile` linker flags. `go install ...@vX.Y.Z` can also report module build information through the runtime fallback.
