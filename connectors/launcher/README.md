# vecnet launcher

The launcher hosts backend plugins behind the contract in `src/backend.ts`, sharing wire types and ports with `../hub-client`. Run `just ln-setup` once per worktree, then `just ln-check` for strict type checking and unit tests (through `build2`, at most two test workers); repository build-box checks use `just bb just ln-check`. This skeleton supplies the backend contract and restartable fakes; daemon assembly and operator documentation arrive in later plan tasks.
