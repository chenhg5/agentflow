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

   Instead of pushing the tag yourself, you can merge the release commit to `main` and run the Release workflow by hand (Actions → Release → Run workflow on `main`, or `POST /repos/chenhg5/agentflow/actions/workflows/release.yml/dispatches` with `{"ref":"main","inputs":{"version":"vX.Y.Z"}}`). It refuses any branch but `main`, a version that is not `vX.Y.Z`, one that differs from `VERSION`, one without `changelogs/<version>.md`, or one whose tag already exists. It builds first and only then tags the head of `main` and publishes, so a failed build leaves no tag. The tag it pushes uses the workflow token, which does not start the workflow a second time.

5. Pushing the tag runs `.github/workflows/release.yml`: it checks that the tag matches `VERSION` and that `changelogs/<version>.md` exists, runs `make release-all` (which runs `make check` first), and publishes the archives and `checksums.txt` as a GitHub prerelease with the changelog as notes. To build locally instead, run `make release-all` and publish `dist/` with `gh release create <version> dist/*.tar.gz dist/*.zip dist/checksums.txt --title <version> --notes-file changelogs/<version>.md --prerelease`.

6. Watch the Release workflow, then check the release page.

7. Verify the tagged install and binary metadata:

   ```bash
   go install github.com/chenhg5/agentflow/cmd/agentflow@v0.0.1
   agentflow version --format table
   ```

The release binary embeds the tag, commit, and UTC build time through the `Makefile` linker flags. `go install ...@vX.Y.Z` can also report module build information through the runtime fallback.
