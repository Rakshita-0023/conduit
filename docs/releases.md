# Releases

Conduit releases are immutable semantic Git tags and Python package versions.
The Python distribution is `conduit-gateway` and uses trusted publishing to
PyPI; no publishing token is stored in the repository.

Use Conventional Commits for release classification:

- `fix:` creates a patch release;
- `feat:` creates a minor release; and
- `!` or `BREAKING CHANGE:` marks a breaking change.

Release automation builds a wheel and source distribution, checks them, creates
an SBOM and provenance attestation where GitHub supports them, publishes to
PyPI using OIDC, and creates a GitHub Release.

On every merge to `main`, Release Please opens or updates a release PR from
Conventional Commit history. Merging that PR creates the immutable tag and
triggers the package release workflow. Users install it with:

```sh
pipx install conduit-gateway
```

For a specific version, use `pipx install conduit-gateway==0.2.0`. Go v0.1.x
artifacts remain preserved by release tags and `go-v0.1-maintenance`.
