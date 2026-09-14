# Security policy

## Supported versions

Security fixes target the latest published stable release. There is no commitment to maintain or backport fixes to older release lines. Reports of suspected vulnerabilities in any version are welcome.

## Reporting a vulnerability

Do not disclose vulnerability details in public issues, discussions, or pull requests.

Use GitHub's **Report a vulnerability** form at
<https://github.com/roie/frigo/security/advisories/new> to send a private report to
this repository's maintainers.

Include the affected Frigo version, operating system, Node.js and Git versions
as relevant, reproduction steps or a minimal proof of concept, and the expected
security impact. Redact credentials and unrelated private data. Coordinate any
public disclosure with the maintainers through the private report.

No response or remediation timeline is promised, and a report does not guarantee a fix.

## Security boundaries

Frigo is convenience tooling, not a security boundary or secret storage. Managed
files remain readable on disk, and their history is local Git data, not encrypted
storage or a backup. Deliberate force-adds, direct index changes, and modified
ignore rules can bypass its ordinary main-Git protections. See the README's
limitations before using Frigo with sensitive files.
