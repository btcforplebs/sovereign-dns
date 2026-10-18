# Releasing

Releases are cut by pushing a `vX.Y.Z` tag. `.github/workflows/release.yml`
then runs `scripts/release-assets.sh`, which refuses unless the tag is the
commit checked out and `install.sh` is byte for byte what that tree builds
(`make-installer.sh --check`, pins included). It signs `install.sh` and
`install.sh.sha256`, and publishes both with notes carrying the hash and the
check a user runs. Nothing is edited by hand.

- [ ] `scripts/check-pins.sh --verify-remote` passes on `main`, and the last
      push to `main` published any image it needed (below).
- [ ] `git tag vX.Y.Z <commit on main>` and `git push origin vX.Y.Z`. Watch
      Actions → **release**. If it refuses, nothing was published; fix on
      `main` and use a new tag.
- [ ] Fetch both files from the release with the line in the notes and see
      `install.sh: OK`; `gh attestation verify install.sh --repo
      btcforplebs/sovereign-dns` passes.
- [ ] Then a second PR changes the README's "first release has not been
      published" sentence and its example tag. That changes the next
      `install.sh`, never the tagged one.

Repository settings, once (Settings → Releases and Rules; not code):

- [ ] Immutable releases on: a published release's files and tag cannot be
      changed or deleted.
- [ ] A tag ruleset on `v*`: no deleting, no moving, no creating except by
      the maintainers.

## The ready-made images

Installs download prebuilt images for every service the stack builds (nvpn
and the web panel, Unbound, the hub and its gates, the docs site) instead of
building them on the device, for every image `docker-compose.prebuilt.yml`
pins (when each has a build for the device's architecture). nvpn alone takes
about 35 minutes to compile on a Raspberry Pi 4. An image left out of the file
builds from source; without the file, everything does, as before.

Each image is built from one folder (`nvpn/`, `unbound/`, `hub/`, `docs/`),
and its tag names a hash of that folder (plus `NVPN_COMMIT` for nvpn;
`scripts/lib/prebuilt-images.sh`). `scripts/check-pins.sh` fails when a pin no
longer matches the tree: any edit in one of those folders, a docs page
included, changes its tag. Then run `scripts/repin-prebuilt.sh` in the same
PR: the new image is not published yet, so it leaves that image out and its
services build from source. After the merge publishes it, repin again to put
it back.

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
      digest, has both architectures, carries the workflow's attestation from
      `main`, and that the commit the attestation names builds that same tag.
      Then it writes the override and regenerates `install.sh`. Images not
      published (or not public) are left out.
- [ ] `scripts/check-pins.sh --verify-remote` passes, then commit and release
      as above.
