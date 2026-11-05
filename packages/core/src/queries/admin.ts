// Admin settings: accounts, login settings, runners and the audit log. The
// API answers not_found to everyone but platform admins.
import type { Schemas } from "@lerryn/api";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";

import { unwrap, useApi, useApiSession } from "../client";
import { shouldRetry } from "../errors";
import { queryKeys } from "./keys";

export type AdminUser = Schemas["AdminUser"];
export type AdminUserPage = Schemas["AdminUserPage"];
export type AdminCreateUserRequest = Schemas["AdminCreateUserRequest"];
export type AdminUpdateUserRequest = Schemas["AdminUpdateUserRequest"];
export type Settings = Schemas["Settings"];
export type UpdateSettingsRequest = Schemas["UpdateSettingsRequest"];
export type AuditEvent = Schemas["AuditEvent"];
export type AuditEventPage = Schemas["AuditEventPage"];
export type Runner = Schemas["Runner"];
export type RunnerToken = Schemas["RunnerToken"];
export type CreateRunnerTokenRequest = Schemas["CreateRunnerTokenRequest"];

const PAGE = 50;

export function useAdminUsers(filter: { q?: string } = {}) {
  const api = useApi();
  const q = filter.q?.trim() ?? "";
  return useInfiniteQuery({
    queryKey: queryKeys.adminUsers({ q }),
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/v1/admin/users", {
          params: { query: { limit: PAGE, ...(q ? { q } : {}), ...(pageParam ? { cursor: pageParam } : {}) } },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<AdminUserPage, string | null>) => ({
      items: data.pages.flatMap((p) => p.items),
      hasMore: data.pages.at(-1)?.next_cursor != null,
    }),
    retry: shouldRetry,
  });
}

function useInvalidateUsers() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: queryKeys.adminUsersAll });
}

export function useAdminCreateUser() {
  const api = useApi();
  const invalidate = useInvalidateUsers();
  return useMutation({
    mutationFn: (body: AdminCreateUserRequest) => unwrap(api.POST("/api/v1/admin/users", { body })),
    onSuccess: invalidate,
  });
}

export function useAdminUpdateUser() {
  const api = useApi();
  const invalidate = useInvalidateUsers();
  return useMutation({
    mutationFn: (v: { userId: string; body: AdminUpdateUserRequest }) =>
      unwrap(api.PATCH("/api/v1/admin/users/{userId}", { params: { path: { userId: v.userId } }, body: v.body })),
    onSuccess: invalidate,
  });
}

export function useAdminResetPassword() {
  const api = useApi();
  return useMutation({
    mutationFn: (v: { userId: string; password: string }) =>
      unwrap(
        api.POST("/api/v1/admin/users/{userId}/password", {
          params: { path: { userId: v.userId } },
          body: { password: v.password },
        }),
      ),
  });
}

export function useAdminSettings() {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.adminSettings,
    queryFn: () => unwrap(api.GET("/api/v1/admin/settings")),
    retry: shouldRetry,
  });
}

export function useUpdateAdminSettings() {
  const session = useApiSession();
  const api = session.client;
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateSettingsRequest) => unwrap(api.PATCH("/api/v1/admin/settings", { body })),
    onMutate: session.capture,
    onSuccess: (settings, _body, isCurrent) => {
      if (!isCurrent?.()) return;
      queryClient.setQueryData(queryKeys.adminSettings, settings);
      // Open registration shows on the sign-in pages through discover.
      void queryClient.invalidateQueries({ queryKey: queryKeys.discover });
    },
  });
}

export interface AuditFilter {
  result?: "success" | "failure" | "denied";
  action?: string;
}

export function useAuditEvents(filter: AuditFilter = {}) {
  const api = useApi();
  const key = { result: filter.result ?? null, action: filter.action?.trim() ?? "" };
  return useInfiniteQuery({
    queryKey: queryKeys.auditEvents(key),
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/v1/admin/audit-events", {
          params: {
            query: {
              limit: PAGE,
              ...(key.result ? { result: key.result } : {}),
              ...(key.action ? { action: key.action } : {}),
              ...(pageParam ? { cursor: pageParam } : {}),
            },
          },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<AuditEventPage, string | null>) => ({
      items: data.pages.flatMap((p) => p.items),
      hasMore: data.pages.at(-1)?.next_cursor != null,
    }),
    retry: shouldRetry,
  });
}

export function useRunners() {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: queryKeys.runners,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/admin/runners", {
        params: { query: { limit: 200, ...(pageParam ? { cursor: pageParam } : {}) } },
        signal,
      })),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<Schemas["RunnerPage"], string | null>) => ({
      items: data.pages.flatMap((page) => page.items),
      next_cursor: data.pages.at(-1)?.next_cursor ?? null,
    }),
    retry: shouldRetry,
    refetchInterval: 10_000,
  });
}

export function useUpdateRunner() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (v: { runnerId: string; disabled: boolean }) =>
      unwrap(
        api.PATCH("/api/v1/admin/runners/{runnerId}", {
          params: { path: { runnerId: v.runnerId } },
          body: { disabled: v.disabled },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.runners }),
  });
}

/** Creates a runner registration token; the token exists only in this result. */
export function useCreateRunnerToken() {
  const api = useApi();
  return useMutation({
    gcTime: 0,
    mutationFn: (body: CreateRunnerTokenRequest) => unwrap(api.POST("/api/v1/admin/runner-tokens", { body })),
  });
}
