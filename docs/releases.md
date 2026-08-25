# Releases

Conduit releases are immutable semantic Git tags on the module path
`github.com/Rakshita-0023/conduit`. The Go module is published by the tag; no
separate package registry publish is required.

Use Conventional Commits for release classification:

- `fix:` creates a patch release;
- `feat:` creates a minor release; and
- `!` or `BREAKING CHANGE:` marks a breaking change.

Release automation creates archives, SHA-256 checksums, an SBOM, provenance
attestation, and a GitHub Release. Supported binary targets are Linux and
macOS on amd64 and arm64. Windows is not currently a supported release target.

On every merge to `main`, Release Please opens or updates a release PR from
Conventional Commit history. Merging that PR creates the immutable tag and
GitHub Release; the same workflow then runs the test suite and appends the
GoReleaser archives, checksums, SBOMs, attestations, and Homebrew cask update.

After a tag is indexed, module users can install it with:

```sh
go install github.com/Rakshita-0023/conduit/cmd/conduit@latest
```

For a specific version, replace `@latest` with an existing tag, such as
`@v0.1.1`. The public Go proxy can take time to index a new tag; retry
`GOPROXY=proxy.golang.org go list -m github.com/Rakshita-0023/conduit@vX.Y.Z`
without moving or rewriting the tag.
