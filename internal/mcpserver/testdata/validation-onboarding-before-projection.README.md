# Onboarding call and output schema capture

The original 23-tool fixtures and the three master additions remain frozen.
`validation-onboarding-before-projection.json` and
`output-onboarding-before-projection.json` capture only the three onboarding
tools from pre-diet onboarding commit
`49e0cd5e196efd62d3c440d6de9091c121a68fcd`.

A `git archive` of that commit was extracted into
`/tmp/vecnet-ob-baseline-merge2`. A temporary package-local
`TestCaptureOnboardingCallSchemas` iterated `agentToolSpecs()`, collecting
`spec.input` for each tool and each non-nil `spec.output`, and wrote the two
maps using `json.MarshalIndent`. These inputs have no references, prose or
`hubValues` exceptions, so they are already the full effective call schemas.
No projection code, projected discovery entry or new description table was
used to construct these fixtures.

The capture test ran on x-p35, with both files pulled back by the build-box
wrapper:

```sh
BB_HOST=x-p35 BB_JOBS=2 BB_ROOT=/tmp/vecnet-ob-baseline-merge2 BB_LANE=ob-baseline-merge2 just bb go test -count=1 -run '^TestCaptureOnboardingCallSchemas$' ./internal/mcpserver/
```

The equivalence table compares frozen and live call validators for all six
agent kinds, safe input options, numeric boundaries, every required field,
unknown and human-only fields, and invalid shapes. Request-ID syntax and stale
spec versions deliberately remain shape-valid; the existing precheck and
handler enforce those semantics. Wire tests cover those refusals and their
redaction separately, with no change to authority, retry or output behavior.
The output comparison uses the operator catalog so both new output contracts
are checked along with all original contracts.
