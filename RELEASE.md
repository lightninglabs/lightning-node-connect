# LNC Release Process

This document describes the steps needed to release a new version of LNC binaries.

### System Requirements

1. Android Studio with Android SDK (API level 16 or newer)
2. Xcode (latest version)
3. Go v1.21.0 or newer

### Build Release Binaries

From the root of the project, run the following command. Replace `vX.Y.Z-alpha`
with the next version number (ex: v0.2.0-alpha)

```sh
$ make release tag=vX.Y.Z-alpha
```

When this completes, a `build` dir will be created with four files:

- **lnc-vX.Y.Z-alpha.wasm**: the WASM reproducible binary
- **lnc-vX.Y.Z-alpha-android.zip**: the gomobile library for android
- **lnc-vX.Y.Z-alpha-ios.zip**: the gomobile library for iOS
- **manifest-vX.Y.Z-alpha.txt**: the sha256 hash manifest file

### Sign the manifest and rename the signature file

#### Sign the manifest file using your PGP key.

- Replace `{PGP_EMAIL}` with your email address associated with your PGP key
- Replace `{GITHUB_USERNAME}` with your github username

```sh
$ gpg --default-key {PGP_EMAIL} --output manifest-{GITHUB_USERNAME}-vX.Y.Z-alpha.sig --detach-sign manifest-vX.Y.Z-alpha.txt
```

### Create a tag and push to Github

Using the `-s` option signs the tag with your PGP key

```sh
$ git tag -s vX.Y.Z-alpha -m "lightning-node-connect vX.Y.Z-alpha"
$ git push origin vX.Y.Z-alpha
```

### Create Github Release

On Github create a new release. Select the tag you just pushed, then click the
"Auto-generate release notes" button.

Take the rest of the content from a previous release. Be sure to update the
version number and update the verification examples to use your own PGP key.

In the assets, include these five files:

- lnc-vX.Y.Z-alpha.wasm
- lnc-vX.Y.Z-alpha-android.zip
- lnc-vX.Y.Z-alpha-ios.zip
- manifest-vX.Y.Z-alpha.txt
- manifest-{GITHUB_USERNAME}-vX.Y.Z-alpha.sig

### Deploy the WASM binary to CDN

The `lnc-vX.Y.Z-alpha.wasm` should be deployed to our CDN so it is available
at the url `https://lightning.engineering/lnc-vX.Y.Z-alpha.wasm`.
