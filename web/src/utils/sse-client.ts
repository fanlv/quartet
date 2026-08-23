import { AgentEvent } from '../types';

export interface SSEClientOptions {
  url: string;
  onEvent: (event: AgentEvent) => void;
  onError?: (error: Error) => void;
  onReconnect?: () => void;
  onDisconnect?: () => void;
  // Initial Last-Event-ID handed to the server on the very first
  // connect / reconnect. Use the snapshot endpoint's lastEventSeq.
  // Any event id received from the stream replaces it for subsequent
  // reconnects. Empty string means "start at buffer tail".
  initialLastEventId?: string;
  // Called when the server returns 410 Gone — the resume point has been
  // GC'd and the client must re-fetch the snapshot before re-subscribing.
  // After this fires the client stops reconnecting; the caller is
  // expected to disconnect, fetch the snapshot, and create a new client.
  // The errorMessage is the server's 410 response body (preferring the
  // `error` field of a JSON envelope, falling back to the raw body), so
  // the caller can surface it verbatim if the recovery path also fails.
  onResumePointGone?: (errorMessage: string) => void;
}

export class SSEClient {
  private abortController: AbortController | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private lastOptions: (SSEClientOptions & { body?: string }) | null = null;
  private maxReconnectDelay = 30_000;
  private lastConnectTime = 0;
  private lastAttempt = 0;
  // Last-Event-ID retained across reconnects. Updated as events with an
  // `id:` line arrive so the next reconnect resumes exactly where we
  // dropped off, regardless of whether the gap was a tab-switch / network
  // blip / browser reconnect.
  private lastEventId = '';
  private resumeGoneNotified = false;
  // Set true once we observe an auth rejection (401/403). Stops further
  // reconnect scheduling: with no valid session, every retry would just
  // generate another auth rejection and burn battery on the client. The user
  // must sign in again before a new client is created.
  private authRejected = false;

  private shouldLogReconnectIssue(attempt: number): boolean {
    return attempt < 3 || (attempt & (attempt - 1)) === 0;
  }

  private formatError(err: unknown): string {
    if (err instanceof Error) return err.message;
    return String(err);
  }

  private isAuthRejection(status: number): boolean {
    return status === 401 || status === 403;
  }

  private parseResponseError(status: number, body: string): string {
    const trimmed = body.trim();
    let detail = trimmed;
    if (trimmed) {
      try {
        const parsed = JSON.parse(trimmed);
        if (typeof parsed?.msg === 'string') detail = parsed.msg;
        else if (typeof parsed?.error === 'string') detail = parsed.error;
        else if (typeof parsed?.message === 'string') detail = parsed.message;
      } catch {
        // Keep the original response text for non-JSON bodies.
      }
    }
    return detail ? `HTTP ${status}: ${detail}` : `HTTP ${status}`;
  }

  // handleAuthRejection short-circuits the reconnect loop on 401/403.
  // Without this guard the SSE client keeps retrying the protected
  // endpoint on the exponential-backoff schedule and the server logs
  // endpoint every attempt. We notify via onError
  // and onDisconnect once, then stay quiet until the caller rebuilds us.
  private async handleAuthRejection(options: SSEClientOptions, response: Response): Promise<void> {
    if (this.authRejected) return;
    this.authRejected = true;
    this.lastOptions = null;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    const errBody = await response.text().catch(() => '');
    const errMsg = this.parseResponseError(response.status, errBody);
    console.error(`[SSEClient] auth rejected (${errMsg}); stopping reconnect — sign-in required`);
    options.onError?.(new Error(`SSE auth rejected: ${errMsg}`));
    options.onDisconnect?.();
  }

  private buildHeaders(body: string | undefined): Record<string, string> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'Accept': 'text/event-stream',
    };
    if (this.lastEventId) {
      headers['Last-Event-ID'] = this.lastEventId;
    }
    if (!body) {
      // Don't emit Content-Type on GET — some proxies (and our hertz
      // server) treat a JSON Content-Type on a GET with empty body as
      // a malformed request.
      delete headers['Content-Type'];
    }
    return headers;
  }

  /**
   * Connect and return a promise that resolves once the SSE stream is established
   * (fetch response headers received = server handler is running and subscriber
   * is registered). Event processing continues in the background.
   */
  async connectUntilReady(options: SSEClientOptions & { body?: string }): Promise<void> {
    const { url, body } = options;
    this.lastOptions = options;
    if (options.initialLastEventId !== undefined) {
      this.lastEventId = options.initialLastEventId;
    }
    this.resumeGoneNotified = false;
    this.authRejected = false;
    this.abortController = new AbortController();

    const headers = this.buildHeaders(body);
    console.debug(`[SSEClient][TRACE-SEQ0] fetch url=${url} method=${body ? 'POST' : 'GET'} Last-Event-ID=${JSON.stringify(headers['Last-Event-ID'] ?? '(absent)')} initialLastEventId=${JSON.stringify(options.initialLastEventId ?? '(absent)')}`);

    let response: Response;
    try {
      response = await fetch(url, {
        method: body ? 'POST' : 'GET',
        headers,
        body,
        signal: this.abortController.signal,
      });
    } catch (error) {
      if ((error as Error).name === 'AbortError') return;
      throw error;
    }

    if (!response.ok) {
      if (response.status === 410) {
        await this.handleResumeGone(response);
        return;
      }
      if (this.isAuthRejection(response.status)) {
        await this.handleAuthRejection(options, response);
        return;
      }
      const errBody = await response.text().catch(() => '');
      let errMsg = `HTTP ${response.status}`;
      try {
        const errJson = JSON.parse(errBody);
        if (errJson.error) errMsg = errJson.error;
      } catch {
        if (errBody) errMsg = errBody;
      }
      throw new Error(errMsg);
    }

    // Connection established — server subscriber is registered.
    // Read the stream in the background (fire-and-forget).
    this.lastConnectTime = Date.now();
    this.lastAttempt = 0;
    this.readStream(response, options).then(() => {
      if (this.abortController) {
        // Stream ended unexpectedly — auto-reconnect
        options.onDisconnect?.();
        const elapsed = Date.now() - this.lastConnectTime;
        this.scheduleReconnect(elapsed < 5000 ? 1 : 0);
      }
    }).catch((err) => {
      if ((err as Error).name !== 'AbortError') {
        options.onDisconnect?.();
        options.onError?.(err as Error);
        // Also try reconnecting on read errors
        if (this.abortController) {
          const elapsed = Date.now() - this.lastConnectTime;
          this.scheduleReconnect(elapsed < 5000 ? 1 : 0);
        }
      }
    });
  }

  async connect(options: SSEClientOptions & { body?: string }): Promise<void> {
    const { url, onError, body } = options;
    this.lastOptions = options;
    if (options.initialLastEventId !== undefined) {
      this.lastEventId = options.initialLastEventId;
    }
    this.resumeGoneNotified = false;
    this.authRejected = false;

    this.abortController = new AbortController();

    try {
      const response = await fetch(url, {
        method: body ? 'POST' : 'GET',
        headers: this.buildHeaders(body),
        body,
        signal: this.abortController.signal,
      });

      if (!response.ok) {
        if (response.status === 410) {
          await this.handleResumeGone(response);
          return;
        }
        if (this.isAuthRejection(response.status)) {
          await this.handleAuthRejection(options, response);
          return;
        }
        const errBody = await response.text().catch(() => '');
        let errMsg = `HTTP ${response.status}`;
        try {
          const errJson = JSON.parse(errBody);
          if (errJson.error) errMsg = errJson.error;
        } catch {
          if (errBody) errMsg = errBody;
        }
        throw new Error(errMsg);
      }
      this.lastConnectTime = Date.now();
      this.lastAttempt = 0;
      await this.readStream(response, options);
      if (this.abortController) {
        options.onDisconnect?.();
        const elapsed = Date.now() - this.lastConnectTime;
        this.scheduleReconnect(elapsed < 5000 ? 1 : 0);
      }
    } catch (error) {
      if ((error as Error).name !== 'AbortError') {
        onError?.(error as Error);
      }
    }
  }

  /**
   * Read SSE events from the response stream until the server closes it.
   * Always triggers a reconnect on return — the server only closes the
   * stream on buffer reset/close or handler exit, never as a "normal end".
   */
  private async readStream(response: Response, options: SSEClientOptions): Promise<void> {
    const { onEvent } = options;
    const reader = response.body?.getReader();
    if (!reader) {
      throw new Error('No readable stream');
    }

    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const events = buffer.split('\n\n');
      buffer = events.pop() || '';

      for (const eventBlock of events) {
        if (!eventBlock.trim()) continue;

        const lines = eventBlock.split('\n');
        const dataLines: string[] = [];
        let idLine = '';

        for (const line of lines) {
          if (line.startsWith('data:')) {
            dataLines.push(line.slice(5).trim());
          } else if (line.startsWith('id:')) {
            idLine = line.slice(3).trim();
          }
        }

        const data = dataLines.join('\n');
        if (!data) continue;

        // Persist the event id BEFORE handing the payload to the
        // application. If onEvent throws, the next reconnect should
        // still resume past the event we already saw — duplicating
        // the throwing event would only re-trigger the same error.
        if (idLine) {
          this.lastEventId = idLine;
        }

        try {
          const event = JSON.parse(data) as AgentEvent;
          onEvent(event);
        } catch {
          const preview = data.length > 200 ? `${data.slice(0, 200)}...` : data;
          console.warn(`[SSEClient] failed to parse SSE payload: len=${data.length} preview=${JSON.stringify(preview)}`);
        }
      }
    }

    // Stream ended — server closed the connection (buffer reset/close or handler exit)
    console.debug(`[SSEClient] stream ended: lastEventId=${this.lastEventId}`);
  }

  // handleResumeGone fires the onResumePointGone callback exactly once
  // and stops further reconnect attempts. The caller is expected to
  // disconnect this client and rebuild after re-fetching the snapshot.
  // The server's response body is forwarded to the callback so the
  // caller can surface the original error verbatim when its own recovery
  // path also fails.
  private async handleResumeGone(response: Response): Promise<void> {
    const errBody = await response.text().catch(() => '');
    if (!this.resumeGoneNotified) {
      this.resumeGoneNotified = true;
      const opts = this.lastOptions;
      // Clear lastOptions so any in-flight reconnect timer becomes a no-op.
      this.lastOptions = null;
      if (this.reconnectTimer) {
        clearTimeout(this.reconnectTimer);
        this.reconnectTimer = null;
      }
      const errMsg = this.parseResponseError(response.status, errBody);
      console.warn(`[SSEClient] 410 Gone for resume point id=${this.lastEventId}: ${errMsg || '(no body)'} — caller must re-fetch snapshot`);
      opts?.onResumePointGone?.(errMsg);
    }
  }

  private scheduleReconnect(attempt: number): void {
    if (!this.lastOptions || !this.abortController) return;

    const delay = Math.min(1000 * Math.pow(2, attempt), this.maxReconnectDelay);
    console.debug(`[SSEClient] scheduling reconnect: attempt=${attempt} delay=${delay}ms lastEventId=${this.lastEventId}`);
    this.reconnectTimer = setTimeout(async () => {
      if (!this.lastOptions || !this.abortController) return;

      const options = this.lastOptions;
      this.abortController = new AbortController();
      const { url, body } = options;

      try {
        const response = await fetch(url, {
          method: body ? 'POST' : 'GET',
          headers: this.buildHeaders(body),
          body,
          signal: this.abortController.signal,
        });

        if (!response.ok) {
          if (response.status === 410) {
            await this.handleResumeGone(response);
            return;
          }
          if (this.isAuthRejection(response.status)) {
            await this.handleAuthRejection(options, response);
            return;
          }
          if (this.shouldLogReconnectIssue(attempt)) {
            console.warn(`[SSEClient] reconnect attempt ${attempt + 1} failed: HTTP ${response.status}`);
          }
          this.scheduleReconnect(attempt + 1);
          return;
        }

        this.lastConnectTime = Date.now();
        this.lastAttempt = attempt;
        options.onReconnect?.();

        await this.readStream(response, options);
        if (this.abortController) {
          // If disconnected quickly after connect (<5s), keep increasing backoff
          // to avoid tight reconnect loops when the server is flapping.
          const elapsed = Date.now() - this.lastConnectTime;
          const nextAttempt = elapsed < 5000 ? this.lastAttempt + 1 : 0;
          this.scheduleReconnect(nextAttempt);
        }
      } catch (err) {
        if ((err as Error).name === 'AbortError') return;
        if (this.shouldLogReconnectIssue(attempt)) {
          console.warn(`[SSEClient] reconnect attempt ${attempt + 1} error: ${this.formatError(err)}`);
        }
        options.onDisconnect?.();
        this.scheduleReconnect(attempt + 1);
      }
    }, delay);
  }

  disconnect(): void {
    this.lastOptions = null;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    try {
      if (this.abortController) {
        this.abortController.abort();
        this.abortController = null;
      }
    } catch {
      // Ignore abort errors
    }
  }

  /** Returns true when disconnect() has been called (or connect was never called). */
  isDisconnected(): boolean {
    return this.lastOptions === null && this.abortController === null;
  }
}
