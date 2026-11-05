// Personal settings: profile, password, credentials, notification channels
// and paired phones. Credential values only ever go out, in
// useFillCredentialRequest; the API has none to give back.
import type { Schemas } from "@lerryn/api";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";

import { unwrap, useApi, useApiSession } from "../client";
import { shouldRetry } from "../errors";
import { RESOURCE_POLL_MS } from "../live/live";
import { queryKeys } from "./keys";

export type Credential = Schemas["Credential"];
export type CredentialRequest = Schemas["CredentialRequest"];
export type CredentialGrant = Schemas["CredentialGrant"];
export type NotificationChannel = Schemas["NotificationChannel"];
export type NotificationChannelKind = Schemas["NotificationChannelKind"];
export type NotificationEventType = Schemas["NotificationEventType"];
export type NotificationDelivery = Schemas["NotificationDelivery"];
export type NotificationSubscriptionInput = Schemas["NotificationSubscriptionInput"];
export type CreateNotificationChannelRequest = Schemas["CreateNotificationChannelRequest"];
export type UpdateNotificationChannelRequest = Schemas["UpdateNotificationChannelRequest"];
export type Device = Schemas["Device"];
export type PairingCode = Schemas["PairingCode"];
export type UpdateMeRequest = Schemas["UpdateMeRequest"];
export type ChangePasswordRequest = Schemas["ChangePasswordRequest"];

// ── Profile ──

export function useUpdateMe() {
  const session = useApiSession();
  const api = session.client;
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateMeRequest) => unwrap(api.PATCH("/api/v1/me", { body })),
    onMutate: session.capture,
    onSuccess: (me, _body, isCurrent) => {
      if (isCurrent?.()) queryClient.setQueryData(queryKeys.me, me);
    },
  });
}

export function useChangePassword() {
  const api = useApi();
  return useMutation({
    mutationFn: (body: ChangePasswordRequest) => unwrap(api.POST("/api/v1/me/password", { body })),
  });
}

// ── Credentials ──

/** The user's own credentials, metadata only. */
export function useCredentials() {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: queryKeys.credentials,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/credentials", {
        params: { query: { limit: 200, ...(pageParam ? { cursor: pageParam } : {}) } },
        signal,
      })),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<Schemas["CredentialPage"], string | null>) => ({
      items: data.pages.flatMap((page) => page.items),
      next_cursor: data.pages.at(-1)?.next_cursor ?? null,
    }),
    retry: shouldRetry,
    refetchInterval: RESOURCE_POLL_MS,
  });
}

/** Fill-in requests waiting for the user, loaded one page at a time. */
export function usePendingCredentialRequests() {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: queryKeys.credentialRequests,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/credential-requests", {
        params: { query: { status: "pending", limit: 200, ...(pageParam ? { cursor: pageParam } : {}) } },
        signal,
      })),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<Schemas["CredentialRequestPage"], string | null>) => ({
      items: data.pages.flatMap((page) => page.items),
      next_cursor: data.pages.at(-1)?.next_cursor ?? null,
    }),
    retry: shouldRetry,
    refetchInterval: RESOURCE_POLL_MS,
  });
}

function useInvalidateCredentials() {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.credentials });
    void queryClient.invalidateQueries({ queryKey: queryKeys.credentialRequests });
    void queryClient.invalidateQueries({ queryKey: queryKeys.overviewAll });
    // Task pages show whether their credentials are usable.
    void queryClient.invalidateQueries({ predicate: (q) => q.queryKey[0] === "tasks" && q.queryKey[2] === "credentials" });
  };
}

export function useRevokeCredential() {
  const api = useApi();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (credentialId: string) =>
      unwrap(api.DELETE("/api/v1/credentials/{credentialId}", { params: { path: { credentialId } } })),
    onSuccess: invalidate,
  });
}

export function useRevokeGrant() {
  const api = useApi();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (v: { credentialId: string; taskId: string }) =>
      unwrap(
        api.DELETE("/api/v1/credentials/{credentialId}/grants/{taskId}", {
          params: { path: { credentialId: v.credentialId, taskId: v.taskId } },
        }),
      ),
    onSuccess: invalidate,
  });
}

/**
 * Starts updating a credential: creates a fill-in request for it and
 * returns the request, which the console's fill dialog then fills
 * (tech design 12.4).
 */
export function useRequestCredentialUpdate() {
  const session = useApiSession();
  const api = session.client;
  const queryClient = useQueryClient();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (credential: Pick<Credential, "id" | "purpose" | "fields">) =>
      unwrap(
        api.POST("/api/v1/credential-requests", {
          body: {
            credential_id: credential.id,
            purpose: credential.purpose,
            // The request lasts an hour: the user is at the console to fill it now.
            expires_in_minutes: 60,
            // Only field names are kept with a credential; every field is
            // asked for again as a secret single-line value.
            fields: credential.fields.map((name) => ({ name, secret: true, multiline: false, optional: false })),
          },
        }),
      ),
    onMutate: session.capture,
    onSuccess: (request, _credential, isCurrent) => {
      if (!isCurrent?.()) return;
      // The fill dialog opens on it right away; no need to wait for a fetch.
      queryClient.setQueryData(queryKeys.credentialRequest(request.id), request);
      invalidate();
    },
  });
}

/** One fill-in request, for the console's fill dialog. */
export function useCredentialRequest(requestId: string | null) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.credentialRequest(requestId ?? ""),
    queryFn: () =>
      unwrap(api.GET("/api/v1/credential-requests/{requestId}", { params: { path: { requestId: requestId ?? "" } } })),
    enabled: !!requestId,
    retry: shouldRetry,
    refetchInterval: (query) => query.state.data?.status === "pending" ? RESOURCE_POLL_MS : false,
  });
}

/**
 * Submits what the user typed in the console's fill dialog. The values go
 * out in this request only: the answer is the fulfilled request, and the
 * mutation, which holds them as its variables, leaves the cache as soon as
 * nothing observes it.
 */
export function useFillCredentialRequest() {
  const api = useApi();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (v: { requestId: string; values: Record<string, string> }) =>
      unwrap(
        api.POST("/api/v1/credential-requests/{requestId}/values", {
          params: { path: { requestId: v.requestId } },
          body: { values: v.values },
        }),
      ),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

/** Withdraws a fill-in request nobody is going to fill, such as an abandoned update. */
export function useCancelCredentialRequest() {
  const api = useApi();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (requestId: string) =>
      unwrap(api.DELETE("/api/v1/credential-requests/{requestId}", { params: { path: { requestId } } })),
    onSuccess: invalidate,
  });
}

// ── Notifications ──

export function useNotificationChannels() {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.channels,
    queryFn: () => unwrap(api.GET("/api/v1/notification-channels")),
    retry: shouldRetry,
  });
}

/**
 * Creates a channel. A webhook channel's signing secret exists only in this
 * mutation's result, like a new access token.
 */
export function useCreateNotificationChannel() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    gcTime: 0,
    mutationFn: (body: CreateNotificationChannelRequest) => unwrap(api.POST("/api/v1/notification-channels", { body })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.channels }),
  });
}

export function useUpdateNotificationChannel() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (v: { channelId: string; body: UpdateNotificationChannelRequest }) =>
      unwrap(
        api.PATCH("/api/v1/notification-channels/{channelId}", { params: { path: { channelId: v.channelId } }, body: v.body }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.channels }),
  });
}

export function useDeleteNotificationChannel() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (channelId: string) =>
      unwrap(api.DELETE("/api/v1/notification-channels/{channelId}", { params: { path: { channelId } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.channels }),
  });
}

export function useTestNotificationChannel() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (channelId: string) =>
      unwrap(api.POST("/api/v1/notification-channels/{channelId}/test", { params: { path: { channelId } } })),
    onSettled: (_d, _e, channelId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.channels });
      void queryClient.invalidateQueries({ queryKey: queryKeys.deliveries(channelId) });
    },
  });
}

export function useNotificationDeliveries(channelId: string | null) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: queryKeys.deliveries(channelId ?? ""),
    enabled: !!channelId,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        api.GET("/api/v1/notification-channels/{channelId}/deliveries", {
          params: { path: { channelId: channelId! }, query: { limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } },
          signal,
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<Schemas["NotificationDeliveryPage"], string | null>) => ({
      items: data.pages.flatMap((page) => page.items),
      next_cursor: data.pages.at(-1)?.next_cursor ?? null,
    }),
    retry: shouldRetry,
  });
}

// ── Phones ──

export function useDevices() {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.devices,
    queryFn: () => unwrap(api.GET("/api/v1/me/devices")),
    retry: shouldRetry,
    // A phone that just scanned the code shows up without a reload.
    refetchInterval: 10_000,
  });
}

export function useRevokeDevice() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (deviceId: string) =>
      unwrap(api.DELETE("/api/v1/me/devices/{deviceId}", { params: { path: { deviceId } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.devices }),
  });
}

/** Makes a one-time pairing code; making a new one voids the previous one. */
export function useCreatePairingCode() {
  const api = useApi();
  return useMutation({
    gcTime: 0,
    mutationFn: () => unwrap(api.POST("/api/v1/auth/pairing")),
  });
}
