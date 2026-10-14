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

## The ready-made nvpn image

Installs download a prebuilt nvpn image instead of compiling nvpn (about 35
minutes on a Raspberry Pi 4) whenever `docker-compose.prebuilt.yml` exists
and pins an image for the device's architecture. Without that file, every
install builds from source, as before.

`scripts/check-pins.sh` fails when the pin no longer matches the tree: a new
`NVPN_COMMIT`, or any edit under `nvpn/`, changes the tag the tree builds
(`scripts/lib/nvpn-image.sh`). Then either repin, or delete
`docker-compose.prebuilt.yml` and regenerate `install.sh` so installs build
from source until the new image exists.

To publish and pin one:

- [ ] On `main`, at the commit to release: Actions → **nvpn image** → Run
      workflow, with **publish** ticked. It builds linux/amd64 and
      linux/arm64 natively and pushes them to
      `ghcr.io/btcforplebs/sovereign-dns-nvpn`, then prints the digest.
- [ ] First publish only: make the package public in its GitHub settings.
      A private package makes every install fall back to source.
- [ ] `scripts/repin-prebuilt.sh sha256:<digest>`. It checks with ghcr.io,
      anonymously, that the digest is real, has both architectures, and is
      what this tree's tag points at, then writes the override and
      regenerates `install.sh`.
- [ ] `scripts/check-pins.sh --verify-remote` passes, then commit and release
      as above.
