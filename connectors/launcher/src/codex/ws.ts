// One framing implementation for Unix sockets and the Windows Codex proxy.
export { connectUnixWebSocket, connectStreamWebSocket } from '../backends/codex-app-server/ws.ts';
export type { WsConn, WsOptions } from '../backends/codex-app-server/ws.ts';
