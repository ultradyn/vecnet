# 35: No way to disable or rotate a principal credential

Found 2026-11-04 retiring pag-chat's `pagd-dryrun` principal (prn_01M6JFV1CNC1F624BM81T0NDAT). The admin API has peer disable/enable/rotate but nothing for principals: a retired principal's secret keeps authenticating (it can still list and PATCH peers it created). Workaround applied: limits set to max_peers 0, creates_per_hour 1, kinds [] (creates_per_hour 0 is refused with 422), and its two peers disabled.

Proposed: `POST /v1/admin/principals/{id}/disable|enable` (auth refuses a disabled principal; its peers keep working unless disabled separately) and `POST /v1/admin/principals/{id}/rotate` (new secret, printed once), with CLI `principal disable|enable|rotate`, audit rows, and PROTOCOL §18.4 text. Ask pag-chat to delete the dry-run credential from /dev/shm/plan8-vecnet.sqlite meanwhile.
