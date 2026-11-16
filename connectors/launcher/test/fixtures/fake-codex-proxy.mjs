// A raw-byte stand-in for `codex app-server proxy`, not a JSONL app-server.
// It connects only to a disposable test pipe supplied out of band.
import { createConnection } from 'node:net';
const socket = createConnection(process.env.FAKE_CODEX_PROXY_PIPE);
socket.on('error', () => process.exit(1));
process.stdin.pipe(socket); socket.pipe(process.stdout);
socket.on('end', () => process.exit(0));
process.stdin.on('end', () => socket.end());
