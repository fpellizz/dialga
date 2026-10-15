export interface SseMessage {
  event: string;
  data: string;
}

/**
 * Returns a function that accepts text/event-stream chunks as they arrive and
 * calls onMessage for every complete message. EventSource cannot send an
 * Authorization header, so the engine streams over a fetch() response.
 */
export function createSseParser(onMessage: (message: SseMessage) => void): (chunk: string) => void {
  let buffer = "";
  return (chunk) => {
    buffer += chunk.replace(/\r\n/g, "\n");
    for (;;) {
      const end = buffer.indexOf("\n\n");
      if (end === -1) {
        return;
      }
      const block = buffer.slice(0, end);
      buffer = buffer.slice(end + 2);
      let event = "message";
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) {
          event = line.slice(6).trim();
        } else if (line.startsWith("data:")) {
          data.push(line.slice(5).trimStart());
        }
      }
      if (data.length > 0) {
        onMessage({ event, data: data.join("\n") });
      }
    }
  };
}
