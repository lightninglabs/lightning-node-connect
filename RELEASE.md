# LNC Release Process

This document describes the steps needed to release a new version of LNC binaries.

### System Requirements

1. Android Studio with Android SDK (API level 16 or newer)
2. Xcode (latest version)
3. Go v1.22.3 or newer
4. gomobile (https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile)
5. javac version 1.7 or higher (Included in Java Development Kit 7+)

#### Android Studio SDK tools requirements

Ensure that NDK is installed for the Android Studio SDK tools.
To install NDK in Android Studio, navigate to Preferences (or Settings) |
Appearance & Behavior | System Settings | Android SDK. Navigate to the SDK Tools
tab, mark `NDK (Side by side)` and then click the "Apply" button.

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

For the signing commands below to work without modifying the path, you first
need navigate `build` dir that was created with the `make release` command
above.

```sh
$ cd build
```

#### Sign the manifest file using your PGP key.

- Replace `{PGP_EMAIL}` with your email address associated with your PGP key
- Replace `{GITHUB_USERNAME}` with your github username

```sh
$ gpg --default-key {PGP_EMAIL} --output manifest-{GITHUB_USERNAME}-vX.Y.Z-alpha.sig --detach-sign manifest-vX.Y.Z-alpha.txt
```

#### Create an Open Timestamp for the signed manifest

Go to https://opentimestamps.org. Upload the newly generated
`manifest-{GITHUB_USERNAME}-vX.Y.Z-alpha.sig` signature file, and download the
resulting `ots` file.

### Create a tag and push to Github

First, double check that the release you just created, was based on the latest
upstream master branch commit. Also verify that you are currently based on the
on the latest master branch commit when creating any tags!

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

In the assets, include these six files:

- lnc-vX.Y.Z-alpha.wasm
- lnc-vX.Y.Z-alpha-android.zip
- lnc-vX.Y.Z-alpha-ios.zip
- manifest-vX.Y.Z-alpha.txt
- manifest-{GITHUB_USERNAME}-vX.Y.Z-alpha.sig
- manifest-{GITHUB_USERNAME}-vX.Y.Z-alpha.sig.ots

### Deploy the WASM binary to CDN

The `lnc-vX.Y.Z-alpha.wasm` should be deployed to our CDN so it is available
at the url `https://lightning.engineering/lnc-vX.Y.Z-alpha.wasm`.
