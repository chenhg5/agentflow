# Releasing AgentFlow

Versions follow semver with a `v` prefix. Before 1.0, minor and patch releases may change the workflow schema or CLI contract. The GitHub Releases page is the source of truth.

## Checklist

1. Update `changelogs/unreleased.md` into `changelogs/<version>.md` and add a new empty `unreleased.md`.
2. Set `VERSION := <version>` in `Makefile`.
3. Run `make check` and verify `go run ./cmd/agentflow version --format json`.
4. Commit the release files:

   ```bash
   git add -A
   git commit -m "release: v0.0.1"
   git tag -a v0.0.1 -m "v0.0.1"
   git push origin main v0.0.1
   ```

5. Build the tagged source:

   ```bash
   make release-all
   ```

   This creates archives for macOS, Linux, and Windows on amd64 and arm64, plus `dist/checksums.txt`.

6. Publish the preview release:

   ```bash
   gh release create v0.0.1 dist/*.tar.gz dist/*.zip dist/checksums.txt \
     --title v0.0.1 --notes-file changelogs/v0.0.1.md --prerelease
   ```

7. Verify the tagged install and binary metadata:

   ```bash
   go install github.com/chenhg5/agentflow/cmd/agentflow@v0.0.1
   agentflow version --format table
   ```

The release binary embeds the tag, commit, and UTC build time through the `Makefile` linker flags. `go install ...@vX.Y.Z` can also report module build information through the runtime fallback.
