# WinGet packaging

This directory will hold the [WinGet](https://learn.microsoft.com/windows/package-manager/)
manifest files that get submitted to
[`microsoft/winget-pkgs`](https://github.com/microsoft/winget-pkgs) once
GoKD has a tagged release.

## Status

**Not yet submitted.** WinGet requires a stable, publicly downloadable
release artifact. We will wait until `v0.1.0` (or later) has been tagged
and published via the `release.yml` workflow before populating
`manifests/` and opening a PR upstream.

## Intended layout

The standard layout for a single-package multi-file manifest is:

```
packaging/winget/manifests/
  n/nijosmsft/gokd/<version>/
    nijosmsft.gokd.installer.yaml
    nijosmsft.gokd.locale.en-US.yaml
    nijosmsft.gokd.yaml
```

See the WinGet manifest schema and authoring guide:

- <https://learn.microsoft.com/windows/package-manager/package/>
- <https://github.com/microsoft/winget-pkgs/tree/master/doc>
- Schema: <https://github.com/microsoft/winget-cli/tree/master/schemas/JSON/manifests>

## TODO before first submission

- [ ] Cut `v0.1.0` (or later) via a `v*` tag on `main`. This triggers
      `.github/workflows/release.yml`, which publishes a
      `gokd-<tag>-windows-amd64.zip` plus `SHA256SUMS.txt`.
- [ ] Extract the SHA256 from `SHA256SUMS.txt` and paste it into the
      installer manifest's `InstallerSha256` field.
- [ ] Decide on `InstallerType`. The current release is a portable zip
      (no MSI / EXE installer), so the manifest will likely use
      `InstallerType: zip` with a `NestedInstallerType` plus the binary
      relative path inside the zip. Confirm against the latest schema
      before submission.
- [ ] Note SmartScreen / signing posture in the PR description: the
      release binaries are **not** Authenticode-signed yet, so users
      will see SmartScreen warnings on first launch. We have no
      reputation with Microsoft SmartScreen until that changes.
- [ ] Run `winget validate manifests/...` and
      `winget install --manifest manifests/...` locally before opening
      the upstream PR.
- [ ] Open a PR to `microsoft/winget-pkgs` from a fork; expect the
      automated CI there to re-validate the manifest and the
      installer URL.
