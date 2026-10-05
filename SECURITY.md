# Security

Please report a security problem privately, not in a public issue: use
**Report a vulnerability** on this repository's Security tab, or write to
info@precomputing.com.

Precomputing is an alpha. It hasn't had an outside security review yet, so
don't put it in front of anything that matters. Two limits matter most: the
HTTP server (`precomputing serve`) takes tokens but has no TLS, so outside
localhost it belongs behind a proxy that adds it; and Traces masks secrets by
pattern, so a secret of a shape it has no pattern for is stored as it came.
