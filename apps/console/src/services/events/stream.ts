export function connectEventStream(onUpdate: () => void) {
  const stream = new EventSource("/api/v1/events/stream");
  stream.addEventListener("update", onUpdate);
  stream.onerror = () => stream.close();
  return stream;
}
