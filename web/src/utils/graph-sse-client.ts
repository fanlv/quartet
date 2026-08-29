import type { AgentEvent } from '../types';
import { SSEClient } from './sse-client';

export type GraphSSEReconcileReason = 'connected' | 'reconnected' | 'resumePointGone';

export interface GraphSSEClientOptions {
  url: string | (() => string);
  onEvent: (event: AgentEvent) => void;
  onReconcile: (reason: GraphSSEReconcileReason, resumeError?: string) => void | Promise<void>;
  onError: (error: Error) => void;
  onDisconnect?: () => void;
}

// GraphSSEClient owns the snapshot/stream handshake shared by the graph run
// canvas and graph chat page. A fresh stream starts at the live buffer tail, so
// every successful initial connection and reconnect is followed by an
// authoritative snapshot reconcile. If a resume cursor has expired, the dead
// stream is discarded, the snapshot is reconciled, and a fresh tail
// subscription is created automatically.
export class GraphSSEClient {
  private client: SSEClient | null = null;
  private stopped = true;
  private connectionGeneration = 0;
  private resumeRecovery: Promise<void> | null = null;

  constructor(private readonly options: GraphSSEClientOptions) {}

  connect(): void {
    if (!this.stopped) return;
    this.stopped = false;
    this.connectFresh();
  }

  disconnect(): void {
    this.stopped = true;
    this.connectionGeneration += 1;
    this.client?.disconnect();
    this.client = null;
    this.resumeRecovery = null;
  }

  private connectFresh(): void {
    if (this.stopped) return;

    const generation = ++this.connectionGeneration;
    this.client?.disconnect();
    const client = new SSEClient();
    this.client = client;
    let connectionRejected = false;

    void client.connectUntilReady({
      url: this.options.url,
      initialLastEventId: '0',
      onEvent: (event) => {
        if (this.isCurrent(client, generation)) this.options.onEvent(event);
      },
      onError: (error) => {
        if (!this.isCurrent(client, generation)) return;
        connectionRejected = true;
        this.options.onError(error);
      },
      onDisconnect: () => {
        if (this.isCurrent(client, generation)) this.options.onDisconnect?.();
      },
      onReconnect: () => {
        if (!this.isCurrent(client, generation)) return;
        void this.reconcile('reconnected');
      },
      onResumePointGone: (errorMessage) => {
        if (!this.isCurrent(client, generation)) return;
        connectionRejected = true;
        void this.recoverResumePoint(client, generation, errorMessage);
      },
    }).then(() => {
      if (!this.isCurrent(client, generation) || connectionRejected) return;
      void this.reconcile('connected');
    }).catch((error) => {
      if (!this.isCurrent(client, generation)) return;
      this.options.onError(error instanceof Error ? error : new Error(String(error)));
    });
  }

  private isCurrent(client: SSEClient, generation: number): boolean {
    return !this.stopped && this.client === client && this.connectionGeneration === generation;
  }

  private async reconcile(reason: GraphSSEReconcileReason, resumeError?: string): Promise<void> {
    try {
      await this.options.onReconcile(reason, resumeError);
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      const message = resumeError
        ? `Resume point expired: ${resumeError}; Snapshot reload failed: ${detail}`
        : detail;
      this.options.onError(new Error(message));
    }
  }

  private recoverResumePoint(client: SSEClient, generation: number, errorMessage: string): Promise<void> {
    if (this.resumeRecovery) return this.resumeRecovery;

    client.disconnect();
    this.resumeRecovery = (async () => {
      await this.reconcile('resumePointGone', errorMessage || 'HTTP 410');
      if (!this.isCurrent(client, generation)) return;
      this.resumeRecovery = null;
      this.connectFresh();
    })().finally(() => {
      this.resumeRecovery = null;
    });
    return this.resumeRecovery;
  }
}
