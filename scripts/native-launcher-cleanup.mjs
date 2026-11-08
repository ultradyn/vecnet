// Private recovery material remains available until both the job and its exact
// run-owned service are proven settled. Operations are injected for fault tests.
export async function cleanupNativeSmoke(state, ops) {
  let safe = false;
  try {
    if (state.installationAttempted && !state.installationConfirmed) throw new Error('installation unconfirmed; preserve recovery files');
    if (state.launchAttempted && !state.jobResolved) throw new Error('launch outcome unresolved; preserve recovery files');
    if (!state.installationConfirmed) throw new Error('installation unconfirmed; preserve recovery files');
    if (state.jobResolved) await ops.settleJob();
    await ops.verifyUnit();
    await ops.stopUnit();
    await ops.verifyStopped();
    await ops.removeUnit();
    await ops.reloadManager();
    safe = true;
  } finally {
    if (safe) await ops.removePrivateFiles();
    else await ops.retain();
  }
  return safe;
}
