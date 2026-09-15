# Bead br-GI-12-01: MIT license at the root, and a README section that names it

- **Priority**: P2
- **Dependencies**: none
- **Blocks**: none

## Description

No `LICENSE` file means default copyright — all rights reserved — so the repo grants nothing to a
forker or a contributor. This bead adds the grant.

MIT over Apache-2.0 because the requirement is permission plus attribution, and MIT's single
condition ("the above copyright notice and this permission notice shall be included in all copies
or substantial portions of the Software") *is* that attribution clause. Apache-2.0 adds a patent
grant and a NOTICE mechanism the project has no use for at this size. 0BSD/public-domain is the
opposite of the requirement: it lets a redistribution strip authorship.

The README gains a short `## License` section so the terms are visible without opening the file,
and states that contributions come in under the same terms — without that sentence a contributor
has no statement of what their patch is licensed under.

Out of scope: per-file SPDX headers. GitHub, `pkg.go.dev`, and license scanners all read the root
file; headers are churn across every source file for no additional effect.

## Outcome Definition

- `LICENSE` exists at the repo root and MIT-licenses the work under the copyright holder named there.
- `README.md` ends with a `## License` section linking to it and covering contributions.
- Nothing else changes: no source file is touched, no dependency or build behavior is affected, so
  the existing test suite is unaffected and needs no new test.
