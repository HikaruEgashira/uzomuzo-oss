# ADR-0032: Diet network privacy

## Status

Accepted

## Context

ADR-0014 combines local graph/source analysis with external health data. Sending every direct dependency to public metadata services can disclose private package names. Python wheel fallback is a second outbound path independent of health analysis.

## Decision

Diet accepts `--offline` and private-name prefix patterns. The application checks this policy before both health and wheel requests. Offline mode performs only local analysis and reports unknown health. Private dependencies retain local coupling and graph results with a `Private` lifecycle; their identifiers never enter either outbound path.

CLI composition reads explicit patterns and exported `GOPRIVATE`/`GONOSUMDB` values. The existing Go module-prefix matcher is reused, with malformed patterns rejected before analysis and no pattern values in errors. Matching is case-insensitive because SBOM identifiers can lose case.

The program does not inspect credential-bearing global registry configuration or infer private origin from an ambiguous Python index. Users supply explicit patterns or use offline mode in those environments.

## Consequences

Online behavior for public dependencies remains unchanged. Private and offline health is unknown, not a claim of safety. Offline Python coupling may be less complete without wheel metadata. Local output still contains private identifiers. Future outbound features must honor the same policy and extend the zero-request regression test.
