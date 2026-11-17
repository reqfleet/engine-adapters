import { check } from 'k6';
import exec from 'k6/execution';
import { Counter, Trend } from 'k6/metrics';
import { WebSocket } from 'k6/websockets';

// The mock endpoint sends a greeting, then one mock-token-NNNNNN per token.
// It must finish with WebSocket close code 1000, not an abrupt TCP disconnect.
const url = __ENV.LLM_WS_URL ||
  'wss://sgra.reqfleet.io/websocket?tokens_per_second=60&ttft_ms=200';
const streamTimeoutMS = 180000;

const ttft = new Trend('llm_time_to_first_token', true);
const tokens = new Counter('llm_token_events');
const interTokenDuration = new Trend('llm_inter_token_duration', true);
const responseDuration = new Trend('llm_response_duration', true);
const receivedBytes = new Counter('request_received_bytes');
const receivedByteTags = { url };
const utf8 = new TextEncoder();

export const options = {
  ext: {
    reqfleet: {
      ttft: 'llm_time_to_first_token',
      tps: 'llm_token_events',
      interToken: 'llm_inter_token_duration',
      llmResponse: 'llm_response_duration',
      receivedBytes: 'request_received_bytes',
    },
  },
  thresholds: { checks: ['rate==1'] },
};

// Replace this parser for your endpoint's protocol. Transport messages are not
// automatically tokens; this mock explicitly emits one token per matching message.
function isToken(message) {
  if (message === '' || message === 'websocket connected') return false;
  if (typeof message !== 'string' || !/^mock-token-[0-9]{6}$/.test(message)) {
    throw new Error('Unexpected mock token payload');
  }
  return true;
}

export default function () {
  const started = exec.instance.currentTestRunDuration;
  let received = 0;
  let previousTokenTime;
  let opened = false;
  let failure = '';
  const socket = new WebSocket(url);
  const timeout = setTimeout(() => {
    if (failure === '') failure = 'Stream deadline exceeded';
    socket.close();
  }, streamTimeoutMS);

  socket.onopen = () => {
    opened = true;
  };

  socket.onmessage = (event) => {
    const now = exec.instance.currentTestRunDuration;
    // Count all received text payloads, including non-token/control messages.
    // This excludes WebSocket/TLS overhead and replaces native data_received.
    if (typeof event.data === 'string') {
      receivedBytes.add(utf8.encode(event.data).byteLength, receivedByteTags);
    }
    if (failure) return;
    try {
      if (!isToken(event.data)) return;
    } catch (error) {
      failure = error.message;
      socket.close();
      return;
    }

    if (received === 0) ttft.add(now - started);
    else interTokenDuration.add(now - previousTokenTime);
    previousTokenTime = now;
    tokens.add(1); // Includes the first token; adapter records tps/status=200.
    received++;
  };

  socket.onerror = (event) => {
    if (failure === '') failure = `WebSocket transport error: ${event.error}`;
  };

  socket.onclose = (event) => {
    clearTimeout(timeout);
    const completed = failure === '' && event.code === 1000 && received > 0;
    check(completed, { 'LLM stream completed': (ok) => ok });
    if (completed) {
      responseDuration.add(exec.instance.currentTestRunDuration - started);
    } else {
      // Preserve any received tokens/TTFT, but do not invent completion latency.
      console.error(`${failure || 'Incomplete stream'}; phase=${opened ? 'stream' : 'handshake'}; ` +
        `close=${event.code === undefined ? 'none' : event.code}; reason=${JSON.stringify(event.reason)}; ` +
        `tokens=${received}; elapsed_ms=${exec.instance.currentTestRunDuration - started}`);
    }
  };
}
