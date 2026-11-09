# Security policy

## Supported versions

Security fixes go into the latest release only. The project is maintained by one person, so older
releases may not be patched. If you are on an older version, upgrade to the latest release to pick up
a fix.

The library needs a supported version of Go. Problems in the Go standard library or toolchain should
be reported to the [Go project](https://go.dev/doc/security/policy), not here.

## Reporting a vulnerability

> [!WARNING]
> Please do not open a public issue, discussion or pull request for a security problem.

Report it privately through
[GitHub's private vulnerability reporting](https://github.com/dangrier/aws-embedded-metrics-golang/security/advisories/new)
(the **Report a vulnerability** button on the repository's **Security** tab).

Please include:

- what the problem is and what an attacker could do with it
- the version or commit you tested against, and your Go version
- the smallest code or input that reproduces it
- any ideas you have for a fix

## What to expect

This is a volunteer project, so these are goals rather than promises:

- **Within 5 business days:** a reply confirming the report has been received.
- **Within 14 days:** an assessment of whether it is a vulnerability, and how serious it is.
- **Within 90 days:** a fixed release for confirmed problems, often much sooner. If a fix will take
  longer, I'll tell you why and agree a new date with you.

I'll keep you updated in the private advisory, and let you check the fix before it is released.
Once a fix is out, I'll publish a GitHub Security Advisory, ask for a CVE where it makes sense, and
report it to the [Go vulnerability database](https://go.dev/security/vuln/) so `govulncheck` users
are told. You'll be credited in the advisory unless you would rather not be.

Please give me a reasonable chance to release a fix before you share details publicly.

## Scope

This library turns your metrics into JSON log lines in the CloudWatch Embedded Metric Format and
writes them to an `io.Writer`. It makes no network calls and handles no credentials. Problems in
scope include:

- input that makes the library write malformed JSON, or JSON that can inject or overwrite fields
  in the log line (for example through metric names, dimension values or properties)
- input that causes a panic, a hang, or unbounded memory or CPU use
- data races or other unsafe behaviour when a logger is used from several goroutines
- a vulnerable dependency that is actually reachable through this library

Out of scope:

- sending untrusted or sensitive data as metrics, dimensions or properties on purpose. Anything you
  pass in ends up in your logs, so do not log secrets or personal data.
- CloudWatch, CloudWatch Logs, Lambda or other AWS services. Report those to
  [AWS](https://aws.amazon.com/security/vulnerability-reporting/).
- vulnerabilities in your own application, log shipping or IAM setup
- findings from automated scanners with no way to exploit them through this library

## Safe harbour

I consider security research carried out in good faith under this policy to be authorised. That
means research that:

- follows this policy, including reporting privately and giving me time to fix the problem
- is done only against your own code, systems and accounts, such as a local copy of this library
- avoids harming other people, their data or their services
- stops and reports promptly once a problem is confirmed, without using it further

This only covers this repository and its code. I can't authorise testing against AWS, GitHub, or
anyone else's systems or applications that use this library. Follow their own policies for those.
If you're unsure whether something is covered, ask in a private advisory before going further.

There is no bug bounty. This library is provided under its [licence](../LICENSE), without
warranty.
