# Contributing to Tipsy

Tipsy is a Go-first Linux compatibility runtime for the official, unmodified
Roblox Android x86-64 client. It provides the Android, JNI, GameActivity, X11,
graphics, input, audio, filesystem, and packaging behavior that the client
actually needs; it does not distribute or modify Roblox.

This guide is the contribution contract. Please read the [README](README.md),
[NOTICE](NOTICE), and [license](LICENSE) before investing in a large change.

## Start here

The supported development target is Linux x86_64-v3 (AVX2), matching official
packages. Tipsy uses Go 1.27.1, a C compiler, pkg-config, Qt 6, and the native
headers listed in the README's
[source-build instructions](README.md#compile-from-source).

```sh
git clone https://github.com/32bitx64bit/Tipsy.git
cd Tipsy

./scripts/bootstrap.sh
GOAMD64=v3 go test ./...
GOAMD64=v3 go build -o bin/tipsy ./cmd/tipsy
GOAMD64=v3 go build -o bin/tipsy-gui ./cmd/tipsy-gui
```

Use a source build only as a development build. `--development` records that
consent locally; it does not skip client-package verification and does not make
a build an official release. See the README before installing desktop entries
or launching a local client.

## Required engineering philosophy

Every accepted contribution follows these rules. A pull request that violates
one of them will be rejected, even if it appears to make the client progress.

1. **Follow observed behavior.** Add compatibility only in response to a
   reproducible, named client requirement. Do not speculatively recreate broad
   parts of Android or add guessed APIs, timers, polling, or fallback paths.
2. **Keep the client official and unmodified.** Do not add engine hooks,
   trampolines, code caves, memory patches, executable patching, anti-cheat
   bypasses, or other client tampering. Fix compatibility at Tipsy's public
   platform boundaries instead.
3. **Fail honestly and safely.** An unsupported API must have an actionable,
   bounded failure path. Do not replace a missing requirement with a no-op,
   fake-success result, fabricated child object, or hidden security bypass.
4. **Make the smallest owned change.** Preserve established contracts and
   unrelated work. Prefer a focused, event-driven fix with a clear owner over
   a broad rewrite or a cross-subsystem workaround.
5. **Protect users and project trust.** Never log, commit, request, or share
   passwords, cookies, tokens, `.ROBLOSECURITY`, personal data, or private
   runtime material. Never commit Roblox packages, assets, native libraries,
   fonts, or proprietary source. Do not copy proprietary implementation from
   another compatibility project.
6. **Separate evidence from claims.** A passing unit test, log line, fixture,
   launch, or present count is not proof of visible login, gameplay, input,
   FPS, latency, or startup improvement. Performance claims need a documented,
   matched measurement of the user-visible workload; correctness comes first.
7. **Preserve release boundaries.** Local builds and diagnostics must remain
   clearly marked as development work. Do not weaken package verification,
   release provenance, source-admission checks, or desktop-integration
   ownership to make a workflow convenient.

## Change workflow

1. Start with a small, reproducible problem. For substantial work, open an
   issue or discussion first and identify the affected subsystem and the user
   behavior that is missing or wrong.
2. Read the nearby code and tests before changing an interface. Keep a change
   within the subsystem that owns the behavior; ask maintainers before moving
   a boundary or combining unrelated cleanup with functional work.
3. Implement the narrowest change that explains the observed failure. Keep
   unsupported paths explicit and diagnostic output redacted.
4. Add or update focused tests that cover the fixed contract, including failure
   behavior where relevant. Do not add real client files, account material, or
   copied proprietary data as test fixtures.
5. Format, test, inspect your diff, and only then open a pull request. Do not
   include drive-by reformatting, generated build products, or unrelated edits.

## Tests and validation

Run the smallest relevant package tests while iterating. Before requesting
review, run the broad checks your machine can support:

```sh
gofmt -w <changed-go-files>
GOAMD64=v3 go vet -unsafeptr=false ./...
GOAMD64=v3 go test -count=1 ./...
GOAMD64=v3 go build -o bin/tipsy ./cmd/tipsy
GOAMD64=v3 go build -o bin/tipsy-gui ./cmd/tipsy-gui
git diff --check
```

The CI-equivalent native pass is `scripts/ci-test-build.sh`; it requires the
native dependencies and Xvfb described in the README. CI also verifies that
all Go files are already formatted and runs the supported distribution matrix.

For a runtime change, state exactly what was observed, what command or test
was used, and what remains unverified. A manual X11 check may use your own
lawfully obtained, verified client, for example:

```sh
DISPLAY=:0 bin/tipsy launch --development
```

Never attach client binaries, logs containing private information, or account
data to an issue or pull request. Redact sensitive output rather than copying
it into a report.

## Pull requests

Keep each PR reviewable and single-purpose. In its description, include:

- The user-visible problem or exact compatibility boundary addressed.
- The approach and why it is limited to the owning subsystem.
- Tests and manual validation run, including any environmental limitations.
- Any behavior that remains unverified; do not turn an inference into a claim.
- Compatibility, privacy, security, and release-trust risks, plus how the
  change fails safely.

Use a clear title and write commit messages that describe the change, not just
the symptom. Reviewers may ask for a narrower patch, additional evidence, or a
separate follow-up for cleanup. Passing CI is necessary but does not override
the required philosophy above.

## Security, privacy, and licensing

Do not disclose a security weakness alongside credentials, client material, or
reproduction data that could harm users. Report it privately through the
repository's security-reporting channel when available; otherwise ask a
maintainer for a confidential contact before publishing details.

By submitting a contribution, you confirm that you have the right to submit
it under Tipsy's GPL-3.0-or-later license and that it contains no material the
project cannot lawfully publish.
