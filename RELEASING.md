# Releasing Atlas

A release is one tag, `vX.Y.Z`. Pushing it runs two workflows:

| Workflow | Publishes |
|----------|-----------|
| `release.yml` | Container image `ghcr.io/manziosee/atlas:X.Y.Z` (and `:X.Y`, `:latest`), signed, with SBOM and provenance; Helm chart `oci://ghcr.io/manziosee/charts/atlas`; binaries for Linux, macOS and Windows (amd64 and arm64) with checksums; and the GitHub Release, with notes from `CHANGELOG.md`. |
| `publish-clients.yml` | Each client library at version X.Y.Z, to every registry enabled below. |

The Go client needs nothing extra: the tag makes
`go get github.com/manziosee/atlas@vX.Y.Z` work.

## Cutting a release

1. Move the entries under `## [Unreleased]` in `CHANGELOG.md` into a new
   `## [X.Y.Z] - YYYY-MM-DD` section, update the links at the bottom, and
   merge that through a pull request. The release job fails if the section
   is missing.
2. Tag the merged commit on `main` and push the tag:

   ```bash
   git switch main && git pull
   git tag -a vX.Y.Z -m "Atlas vX.Y.Z"
   git push origin vX.Y.Z
   ```

   Don't create the release in GitHub's web UI: the workflow creates it, and
   fails if it already exists.
3. Watch the **Release** and **Publish clients** runs under Actions.

Tags with a suffix, such as `v0.2.0-rc.1`, become GitHub pre-releases.

To publish clients for an existing tag later (for example after enabling a
registry), run **Actions → Publish clients → Run workflow** with that tag.

## One-time registry setup

Each registry is switched on by a repository variable, under **Settings →
Secrets and variables → Actions → Variables**. Until its variable is `true`,
its job is skipped and the rest of the release still succeeds. Add secrets on
the **Secrets** tab of the same page.

### PyPI: `atlas-service-discovery`

Uses trusted publishing, so no token is stored.

1. Create an account at <https://pypi.org> and turn on two-factor
   authentication.
2. **Account settings → Publishing → Add a new pending publisher**:
   PyPI project name `atlas-service-discovery`, owner `manziosee`,
   repository `atlas`, workflow `publish-clients.yml`, environment `pypi`.
3. Set the variable `PUBLISH_PYPI` to `true`.

### npm: `@manziosee/atlas-client`

1. Create an account at <https://www.npmjs.com> with the username
   `manziosee` (the `@manziosee` scope is the username), and turn on
   two-factor authentication.
2. **Access Tokens → Generate New Token → Granular Access Token**, with
   **Read and write** permission on packages, and an expiry.
3. Add it as the secret `NPM_TOKEN`, and set `PUBLISH_NPM` to `true`.

Packages are published with provenance, so npm shows they were built by this
repository's workflow.

### NuGet: `Manziosee.Atlas.Client` and `Manziosee.Atlas.Client.DependencyInjection`

1. Sign in at <https://www.nuget.org>.
2. **API Keys → Create**: scope **Push new packages and package versions**,
   glob pattern `Manziosee.Atlas.Client*`.
3. Add it as the secret `NUGET_API_KEY`, and set `PUBLISH_NUGET` to `true`.

### Maven Central: `io.github.manziosee:atlas-client`

1. Sign in at <https://central.sonatype.com> with GitHub. The namespace
   `io.github.manziosee` is verified automatically for that account.
2. **View Account → Generate User Token**. Add the two values as the secrets
   `MAVEN_CENTRAL_USERNAME` and `MAVEN_CENTRAL_PASSWORD`.
3. Create a signing key and publish its public half:

   ```bash
   gpg --quick-generate-key "Manzi Osee <your-email>" rsa4096 sign 2y
   gpg --list-keys                      # note the key ID
   gpg --keyserver keyserver.ubuntu.com --send-keys <KEY_ID>
   gpg --armor --export-secret-keys <KEY_ID>
   ```

   Add the exported block as the secret `MAVEN_GPG_PRIVATE_KEY` and its
   passphrase as `MAVEN_GPG_PASSPHRASE`.
4. Set `PUBLISH_MAVEN` to `true`.

Maven Central releases are permanent: a version can never be replaced or
deleted.
