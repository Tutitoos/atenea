# OpenCode sidebar footer fixture

`opencode-footer-v1.18.30.tsx` is the unmodified upstream component at
[`v1.18.30`](https://github.com/anomalyco/opencode/blob/v1.18.30/packages/tui/src/feature-plugins/sidebar/footer.tsx),
tag commit `3104c1428ec91f809e5ab86631300de41eb6952e`, Git blob
`c59046a01722d985e71bc17346995f30c673e0ea`.
The upstream MIT notice is preserved in `LICENSE-opencode`.

The file is identical at `v1.18.16` and `v1.18.30`. OpenCode's Homebrew
v1.18.30 binary no longer exposes the `internal:sidebar-footer` module as
plain text. The host-footer test first inspects the installed binary as before.
When its marker is absent, it accepts only version 1.18.30 plus this fixture's
SHA-256 and the widget coverage test. A later opaque version fails until its
tagged source and visible footer have been reviewed again. This fallback
validates published source provenance, not the binary's rendered UI.
