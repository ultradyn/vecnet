export type Priority = "wake" | "wake_idle" | "hold";
export type MessageKind = "message" | "approval_request" | "approval_response" | "job_status";
export interface Attachment {
  sha256: string; size: number; media_type: string; name?: string; origin_url?: string; blob_url?: string;
  blob_id?: string; retention_class?: string; expires_at?: string | null;
  sha256_verified?: boolean; integrity?: { algorithm: string; value: string; verified: boolean };
}
export interface Loop { depth: number; max_depth: number; expires_at: string; sender_chain: string[] }
export interface ApprovalRequest { approval_id: string; question: string; options: string[]; expires_at: string; escalate_to_operator?: boolean }
export interface ApprovalResponse { approval_id: string; decision: string; reason?: string }
export interface Message { sender_info?: { name: string; kind: string }; message_id: string; thread_id: string; in_reply_to: string | null; kind: MessageKind; sender: string; recipients: string[]; priority: Priority; created_at: string; content: { text: string; truncated?: boolean }; attachments: Attachment[]; metadata: Record<string, unknown>; loop: Loop; job_request?: JobRequest; job_status?: JobStatusBody; approval_request?: ApprovalRequest; approval_response?: ApprovalResponse }
export interface ThreadSummary { thread_id: string; title: string | null; participants: string[]; group?: string | null; message_count: number; budget_remaining: number | null; history_access: "from_join" | "full" }
export interface Delivery { delivery_id: string; recipient: string; via: string | null; also_via?: string[]; attempt: number; ingress: string; thread: ThreadSummary; message: Message }
export type EventType = "delivery" | "receipt" | "presence" | "thread_updated" | "directory_updated" | "approval_expired";
export interface VecnetEvent { cursor: string; type: EventType; at: string; data: unknown }
export interface SendRequest { as?: string; locked_participants?: boolean; history_access?: "from_join" | "full"; max_depth?: number; expires_at?: string; metadata?: Record<string, unknown>; job_status?: JobStatusBody; job_request?: JobRequest; idempotency_key: string; thread_id?: string; thread_title?: string; in_reply_to?: string; recipients?: string[]; kind?: MessageKind; priority: Priority; content: { text: string }; attachments?: Attachment[]; approval_request?: ApprovalRequest; approval_response?: ApprovalResponse }
export interface SendResponse { job_id?: string; message_id: string; thread_id: string; deliveries: { delivery_id: string; recipient: string; via: string | null; state: string }[]; spilled: boolean }
export interface DirectoryEntry { created_by?: string; peer_id: string; name: string; kind: string; capabilities: string[]; quota: Record<string, unknown>; location: string | null; presence: string; reachability: string; last_seen: string | null; enrolled_at: string | null; teams: string[] }
export interface GroupInfo { group_id: string; members: string[]; created_by: string; archived: boolean }
export interface PresenceBody { quota?: Record<string, unknown>; state: "idle" | "working" | "blocked" | "offline"; at: string; capabilities?: string[]; location?: string; note?: string }
export interface ReceiptBody { delivery_id: string; state: "processed" | "failed" | "read"; note?: string }


export interface JobRequest { version: 1; job_key: string; operation: string; instructions: string; input: unknown }
export interface JobStatusBody { job_id: string; sequence: number; state: "accepted" | "running" | "succeeded" | "failed" | "cancelled"; output?: unknown }
