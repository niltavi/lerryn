import { createServer, request as httpRequest, type IncomingMessage, type Server } from "node:http";

/** One request a phone session made. */
export interface PhoneRequest {
  method: string;
  path: string;
  status: number;
}

async function readBody(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  return Buffer.concat(chunks);
}

/** A sign-in that opens a phone session: a pairing exchange, or a password login that names a device. */
function isPhoneSignIn(path: string, body: Buffer): boolean {
  if (path.startsWith("/api/v1/auth/pairing/exchange")) return true;
  if (!path.startsWith("/api/v1/auth/login")) return false;
  try {
    return !!(JSON.parse(body.toString("utf8")) as { device?: unknown }).device;
  } catch {
    return false;
  }
}

/**
 * A reverse proxy in front of the server that writes down every request a
 * phone session makes. The deployment's public URL is the proxy, so the
 * pairing link sends the app here.
 */
export class Recorder {
  readonly phone: PhoneRequest[] = [];
  private readonly phoneSessions = new Set<string>();
  private server: Server | null = null;
  private readonly target: URL;

  constructor(target: string) {
    this.target = new URL(target);
  }

  listen(port: number): Promise<void> {
    this.server = createServer((req, res) => {
      void (async () => {
        const path = req.url ?? "/";
        const body = await readBody(req);
        const cookie = /lerryn_session=([^;]+)/.exec(req.headers.cookie ?? "")?.[1];
        const signIn = isPhoneSignIn(path, body);
        const upstream = httpRequest(
          { host: this.target.hostname, port: this.target.port, method: req.method, path, headers: req.headers },
          (up) => {
            const status = up.statusCode ?? 502;
            if (signIn) {
              for (const c of [up.headers["set-cookie"] ?? []].flat()) {
                const value = /^lerryn_session=([^;]+)/.exec(c)?.[1];
                if (value) this.phoneSessions.add(value);
              }
            }
            if (signIn || (cookie && this.phoneSessions.has(cookie))) {
              this.phone.push({ method: req.method ?? "GET", path: path.split("?")[0]!, status });
            }
            res.writeHead(status, up.headers);
            up.pipe(res);
          },
        );
        upstream.on("error", () => {
          if (!res.headersSent) res.writeHead(502);
          res.end();
        });
        upstream.end(body);
      })();
    });
    return new Promise((ok) => this.server!.listen(port, "127.0.0.1", () => ok()));
  }

  /** The phone's requests that would change something; signing in and out excepted. */
  writes(): PhoneRequest[] {
    return this.phone.filter(
      (r) => r.method !== "GET" && r.method !== "HEAD" && !/^\/api\/v1\/auth\/(pairing\/exchange|login|logout)$/.test(r.path),
    );
  }

  close(): Promise<void> {
    return new Promise((ok) => {
      if (!this.server) return ok();
      this.server.closeAllConnections();
      this.server.close(() => ok());
    });
  }
}
