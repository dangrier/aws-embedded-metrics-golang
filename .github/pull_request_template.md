<!--
The title becomes this PR's line in the release notes, so say what changed for users,
like "Add Flush for reusing a logger".
-->

## What and why

<!-- What this changes, and why. -->

Closes #

## Compatibility

<!--
If someone upgrades without changing their code, could anything they rely on behave
differently, even if it isn't documented? This is a wider test than a semver breaking change.
-->

**Breaking in effect?** <!-- No, Low, Possibly or Yes -->

**Likely side effect:** <!-- What someone would notice, such as different output, metrics moving to a new series, new error handler calls or more log lines. "None" if nothing changes. -->

## Release notes

<!--
Release notes are grouped by label. Branches named feat/... or fix/... get enhancement or
bug automatically, and docs and CI changes are labelled for you.

- breaking: changes the API, or changes documented behaviour that works correctly.
  Needs the next major version.
- behaviour-change: Breaking in effect is Low or higher. Add it alongside bug or
  enhancement. Needs at least a minor version.
- enhancement: adds something new. Needs a minor version.
- bug: fixes incorrect behaviour. Needs a patch version.
-->

- [ ] This PR has its release notes label

## Checklist

- [ ] `just check` passes
- [ ] Tests cover the change, and output changes are checked against the EMF spec
- [ ] README and Go doc comments match the change, with an example for new API
- [ ] For output changes, `just fuzz` ran clean
