import type { Priority } from '../../hub-client/src/wire.ts';
import type { BackendCapabilities, SessionState } from './backend.ts';

export type InputPlan = 'start' | 'steer' | 'queue-native' | 'queue-launcher' | 'buffer' | 'refuse';
export function planInput(p: Priority, s: SessionState['kind'] | 'launching', caps: Pick<BackendCapabilities, 'steer' | 'queue'>): InputPlan {
  if (s === 'ended') return 'refuse';
  if (s === 'launching' || p === 'hold') return 'buffer';
  if (s === 'idle' || s === 'parked') return 'start';
  if (p === 'wake' && caps.steer) return 'steer';
  return caps.queue === 'native' ? 'queue-native' : 'queue-launcher';
}
