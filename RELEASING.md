# Releasing

Checklist for publishing a release of `install.sh`. Nothing enforces these
steps: the pre-commit hook is opt-in and there is no CI, so they are on the
person who tags the release.

- [ ] `scripts/make-installer.sh --check` passes **at the tagged commit**
      (`git checkout <tag>` in a clean clone, then run it). It proves that
      `install.sh` is byte for byte what the tree builds, that
      `install.sh.sha256` is its hash, and that the pins pass.
- [ ] Publish `scripts/install.sh` and `scripts/install.sh.sha256` side by
      side, from that same commit.
- [ ] Fetch both from the published URL with `curl` and run
      `shasum -a 256 -c install.sh.sha256` on what came back.
