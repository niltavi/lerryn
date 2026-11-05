import { createLerrynClient, isProblem, type ErrorCode, type Problem, type LerrynClient } from "@lerryn/api";
import { createContext, useContext, useEffect, useMemo, useReducer, type ReactNode } from "react";

/** Error codes from the contract, plus one for requests that never got an answer. */
export type ApiErrorCode = ErrorCode | "network_error";

/** A failed API call. Pages branch on `code`, never on the message text. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode;
  readonly problem: Problem | null;

  constructor(status: number, code: ApiErrorCode, problem: Problem | null, message?: string) {
    super(message ?? problem?.detail ?? problem?.title ?? code);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.problem = problem;
  }

  /** The request ID to quote when reporting a problem, if the server sent one. */
  get requestId(): string | undefined {
    return this.problem?.request_id;
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}

/** Whether err means "this resource does not exist or you may not see it". */
export function isNotFound(err: unknown): boolean {
  return isApiError(err) && err.code === "not_found";
}

/** Whether err means the caller is not signed in. */
export function isUnauthenticated(err: unknown): boolean {
  return isApiError(err) && (err.status === 401 || err.code === "unauthenticated");
}

interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/**
 * Returns the body of a successful openapi-fetch call and throws ApiError
 * otherwise, so TanStack Query sees failures as errors.
 */
export async function unwrap<T>(call: Promise<FetchResult<T>>): Promise<T> {
  let result: FetchResult<T>;
  try {
    result = await call;
  } catch (err) {
    throw new ApiError(0, "network_error", null, err instanceof Error ? err.message : String(err));
  }
  const { data, error, response } = result;
  if (response.ok) return data as T;
  if (isProblem(error)) throw new ApiError(response.status, error.code, error);
  throw new ApiError(response.status, "internal_error", null, response.statusText);
}

/**
 * The client the console uses: same origin as the page, authenticated by
 * the session cookie, so no token ever lives in browser storage.
 */
export function createConsoleClient(baseUrl?: string): LerrynClient {
  const origin = baseUrl ?? (typeof window !== "undefined" ? window.location.origin : "http://localhost");
  return createLerrynClient({ baseUrl: origin, credentials: "same-origin" });
}

interface ApiSession {
  client: LerrynClient;
  capture: () => () => boolean;
  renew: () => void;
  activate: () => void;
  dispose: () => void;
}

function apiSession(client: LerrynClient, onRenew: () => void): ApiSession {
  let generation = {};
  let active = true;
  const renew = () => {
    generation = {};
    onRenew();
  };
  return {
    client,
    capture: () => {
      const captured = generation;
      return () => active && captured === generation;
    },
    renew,
    activate: () => {
      if (!active) {
        active = true;
        renew();
      }
    },
    dispose: () => {
      active = false;
      generation = {};
    },
  };
}

const ApiContext = createContext<ApiSession | null>(null);

export function ApiProvider({ client, children }: { client: LerrynClient; children: ReactNode }) {
  const [generation, advanceGeneration] = useReducer((value: number) => value + 1, 0);
  const session = useMemo(() => apiSession(client, advanceGeneration), [client]);
  const value = useMemo(() => {
    const isCurrent = session.capture();
    // Retained callbacks from an old screen cannot capture a newer login.
    return { ...session, capture: () => isCurrent };
  }, [session, generation]);
  // A different client can name another deployment or carry another token.
  useEffect(() => {
    session.activate();
    return () => session.dispose();
  }, [session]);
  return <ApiContext.Provider value={value}>{children}</ApiContext.Provider>;
}

/** Captures a cache-write guard before a mutation starts; auth changes invalidate it. */
export function useApiSession(): ApiSession {
  const session = useContext(ApiContext);
  if (!session) throw new Error("useApi must be used inside ApiProvider");
  return session;
}

/** The API client from the nearest ApiProvider. */
export function useApi(): LerrynClient {
  return useApiSession().client;
}
