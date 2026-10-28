## Problem and resulting behavior

## Verification

<!-- Commands run, and checks that could not run. -->

## Pre-release checks (release PRs and anything touching signing, packaging, the updater, privacy runtime or UI layout)

CI cannot do these; tick each on a real device before tagging, or state why not.

- [ ] macOS Apple Silicon: install the signed dmg, launch the app, enable the local privacy model and confirm the ONNX Runtime loads (hardened runtime and library validation)
- [ ] Windows: run the NSIS installer, launch, upgrade over the previous version, uninstall cleanly
- [ ] Linux: `apt install` the deb, then in-app update from the previous release; 1.2.x installs must reinstall the deb once
- [ ] UI at narrow window width (mobile-like) and in the light theme: rail, AI panel, asset tree and status bar are usable, no clipped or duplicated buttons
- [ ] Release notes preview shows BREAKING CHANGE footers in full
