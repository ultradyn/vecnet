// Private recovery material remains available until both the job and its exact
// run-owned service are proven settled. Operations are injected for fault tests.
export async function cleanupNativeSmoke(state, ops) {
  let safe = false;
  try {
    await ops.assertHostRetired?.();
    if (state.installationAttempted && !state.installationConfirmed) throw new Error('installation unconfirmed; preserve recovery files');
    if (state.launchAttempted && !state.jobResolved) throw new Error('launch outcome unresolved; preserve recovery files');
    if (!state.installationConfirmed) throw new Error('installation unconfirmed; preserve recovery files');
    for (const operation of [...(state.jobResolved ? ['settleJob'] : []),
      'verifyUnit', 'stopUnit', 'verifyStopped', 'removeUnit', 'reloadManager']) {
      await ops.assertHostRetired?.();
      await ops[operation]();
    }
    await ops.assertHostRetired?.();
    safe = true;
  } finally {
    if (safe) await ops.removePrivateFiles();
    else await ops.retain();
  }
  return safe;
}
