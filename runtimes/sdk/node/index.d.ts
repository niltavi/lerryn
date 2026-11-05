// SPDX-License-Identifier: Apache-2.0

/** Version of the event line format. */
export declare const FORMAT_VERSION: 1;
export declare const MESSAGE_MAX_BYTES: number;
export declare const SUMMARY_MAX_BYTES: number;
export declare const DATA_MAX_BYTES: number;

export type ResultStatus = "ok" | "partial" | "failed";
export declare const RESULT_STATUSES: readonly ResultStatus[];

/** The run's parameters, decoded from LERRYN_PARAMS. */
export declare function params<T extends Record<string, unknown> = Record<string, unknown>>(): T;

/** Reports that the stage started. */
export declare function stageStart(stageId: string, message?: string): void;
/** Reports that the stage finished successfully. */
export declare function stageDone(stageId: string, message?: string): void;
/** Reports that the stage failed. */
export declare function stageFail(stageId: string, message?: string): void;

/**
 * Runs fn as a stage: done when it returns or its promise resolves, failed
 * when it throws or rejects; the error is passed on.
 */
export declare function stage<T>(stageId: string, fn: () => T): T;

/** Reports the business result of the run; data is at most 64 KiB as JSON. */
export declare function result(status: ResultStatus, summary: string, data?: unknown): void;

/** A tool call failed; result holds the tool's error result, if any. */
export declare class ToolError extends Error {
  readonly result?: McpCallToolResult;
}

/** The MCP CallToolResult a tool returns. */
export interface McpCallToolResult {
  content: Array<{ type: string; text?: string; [key: string]: unknown }>;
  structuredContent?: Record<string, unknown>;
  isError?: boolean;
  [key: string]: unknown;
}

export declare const MCP_PROTOCOL_VERSION: string;

/**
 * Calls the MCP tool `name` of the registered MCP tool `tool`, listed in
 * the manifest's tools. The call is recorded in the run's events.
 */
export declare function mcpCall(
  tool: string,
  name: string,
  args?: Record<string, unknown>,
  options?: { headers?: Record<string, string>; timeout?: number },
): Promise<McpCallToolResult>;
