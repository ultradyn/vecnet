import { delivery } from '../../hub-client/testing/fixtures.ts';
import { newUlid } from '../../hub-client/src/ids.ts';
import type { Delivery, Priority } from '../../hub-client/src/wire.ts';

export function launchDelivery(input: unknown, overrides: Partial<Delivery> = {}): Delivery {
  const d = delivery();
  d.delivery_id = `dly_${newUlid(0)}`;
  d.thread.thread_id = `thr_${newUlid(0)}`;
  d.message.thread_id = d.thread.thread_id;
  d.message.message_id = `msg_${newUlid(0)}`;
  d.message.attachments = [];
  d.message.job_request = {version:1,job_key:`launch-${newUlid(0)}`,operation:'launch',instructions:'Complete the requested work.',input};
  d.message.metadata = {'vecnet.job_id':`job_${newUlid(0)}`};
  return {...d,...overrides};
}

export function threadDelivery(jobThread: string, sender: string, text: string, priority: Priority, overrides: Partial<Delivery> = {}): Delivery {
  const d = delivery();
  d.delivery_id = `dly_${newUlid(0)}`;
  d.thread.thread_id = jobThread;
  d.message = {...d.message,message_id:`msg_${newUlid(0)}`,thread_id:jobThread,sender,content:{text},priority,attachments:[],metadata:{}};
  return {...d,...overrides};
}
