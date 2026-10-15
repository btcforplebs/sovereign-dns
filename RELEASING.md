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

## The ready-made images

Installs download prebuilt images for every service the stack builds (nvpn
and the web panel, Unbound, the hub and its gates, the docs site) instead of
building them on the device, whenever `docker-compose.prebuilt.yml` exists and
every image has a build for the device's architecture. nvpn alone takes about
35 minutes to compile on a Raspberry Pi 4. Without that file, every install
builds from source, as before.

Each image is built from one folder (`nvpn/`, `unbound/`, `hub/`, `docs/`),
and its tag names a hash of that folder (plus `NVPN_COMMIT` for nvpn;
`scripts/lib/prebuilt-images.sh`). `scripts/check-pins.sh` fails when a pin no
longer matches the tree: any edit in one of those folders, a docs page
included, changes its tag. Then either repin, or delete
`docker-compose.prebuilt.yml` and regenerate `install.sh` so installs build
from source until the new images exist.

To publish and pin:

- [ ] Merge to `main`. Every push to `main` runs Actions → **prebuilt
      images**, which builds the images whose tag is not on ghcr.io yet,
      for linux/amd64 and linux/arm64 natively, pushes them to
      `ghcr.io/btcforplebs/sovereign-dns-<folder>`, and signs their build
      provenance. (Run workflow with **publish** ticked does the same by hand.)
- [ ] First publish of a package only: make it public in its GitHub
      settings. A private package makes every install fall back to source.
- [ ] `scripts/repin-prebuilt.sh`. For each image it asks ghcr.io,
      anonymously, for the tag this tree names, checks the index hashes to its
      digest, has both architectures, and carries the workflow's attestation
      from `main`, then writes the override and regenerates `install.sh`.
- [ ] `scripts/check-pins.sh --verify-remote` passes, then commit and release
      as above.
