# Envelope Exchange Fixture

Convention document: not an artifact, not an attachment. Two artifacts, one
per adapter, each declaring the full ADR-035 capture envelope —
`provenance`, `sourcePromptHash`, `confidence`, `sourceCommitSha`,
`captureMeta{classifier, dedupeKey, title}` — plus the freshness dates. The
Markdown artifact declares `confidence: 0.0` on purpose: an explicit zero is
a claim, not an absence.