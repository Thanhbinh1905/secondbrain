# Security policy

## Scope

The supported security surface is the released `brain-axi` binary, the installer, and
the Markdown vault format. The CLI is intentionally local and has no model client,
credential store, server, or background process. Explicit forge and upgrade commands
delegate to tools already installed and authenticated by the operator.

Release `checksums.txt` files provide transfer and integrity checking only. They are
not signatures and do not authenticate a publisher. Do not treat a checksum match as
a supply-chain guarantee.

## Reporting

Do not file a public issue for a suspected vulnerability. Report it privately through
the repository's GitHub security advisory page when available. If that page is not
available, contact the repository owner through a private GitHub channel and include:

- the affected version and platform;
- the smallest reproduction or proof of concept;
- the impact and any required local access;
- a suggested mitigation, if known.

Please do not include real vault contents, credentials, tokens, or private forge URLs.

## Support boundary

Windows is not supported by the release workflow or installer. The project makes no
promise of signed release provenance or cross-process vault locking. Those would need
separate product and security decisions before they become supported guarantees.
