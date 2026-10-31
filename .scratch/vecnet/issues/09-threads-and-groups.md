# Thread and group lifecycle

Type: grilling
Status: resolved
Blocked by: 08

## Question

Thread creation (implicit on first message vs explicit), participants and joining, title, mapping to provider threads via `original_thread_id`, when a reply forks a new thread. Teams (operator-managed) vs ad hoc groups (bot-created, 2-4 members, members may add others): lifecycle, naming, visibility to the operator, whether a group implies a thread.

## Answer

Max approved by voice, 2026-11-01 ("yep, sounds good").

- **Threads are implicit.** A send without `thread_id` creates a thread; participants = sender + resolved recipients. Addressing a later message in the thread to a new peer adds that peer as a participant with access to the thread's history.
- **Replies never fork.** A reply stays in its thread. A new topic is a new thread, optionally with `in_reply_to` pointing at a message in another thread (cross-thread reply links are allowed).
- **Teams** are operator-managed, named, stable (`team_…`). **Ad hoc groups** (`grp_…`) are created by any peer via the API with members it is allowed to message; members may add others under the same scopes; the operator sees every group; a group auto-archives after 30 days idle. Neither implies a thread: a message to a group creates or reuses a thread like any recipient set.
- **External mapping.** Connectors own the external-conversation-id ↔ `thread_id` mapping. The hub stores `original_thread_id` on the message and echoes it on deliveries, nothing more.
