// Retry timing shared by the durable event pump and ephemeral activity feed.
export function retryDelay(backoff, random) {
  return Math.min(30000, Math.round(backoff * (0.8 + 0.4 * random())));
}

// Both streams keep visible views fresh. An activity-only outage matters too.
export function connectionState(events, activity) {
  const states = [events, activity];
  return ['stopped', 'reconnecting', 'connecting'].find(state => states.includes(state)) || 'live';
}
