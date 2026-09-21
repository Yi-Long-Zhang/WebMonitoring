// connectEventStream opens an SSE connection to the server's event stream and
// calls onUpdate on every "update" event. When the connection drops (server
// restart, network interruption), it reconnects with exponential backoff
// (1s → 2s → 4s → ... → 30s cap), resetting on each successful connection.
// The returned cleanup function closes the stream permanently — call it on
// component unmount or sign-out so the reconnect loop stops.
export function connectEventStream(onUpdate: () => void): { close: () => void } {
  let stream: EventSource | undefined;
  let retryDelay = 1000;
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  function open() {
    if (stopped) return;
    stream = new EventSource("/api/v1/events/stream");
    stream.addEventListener("update", onUpdate);
    stream.onerror = () => {
      stream?.close();
      stream = undefined;
      if (!stopped) {
        timer = setTimeout(() => { open(); }, retryDelay);
        retryDelay = Math.min(retryDelay * 2, 30000);
      }
    };
    // A successful open resets the backoff so the next drop starts from 1s again.
    stream.addEventListener("open", () => { retryDelay = 1000; });
  }

  open();
  return {
    close() {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
      stream?.close();
    },
  };
}
