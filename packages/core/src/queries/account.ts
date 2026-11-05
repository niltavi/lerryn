import type { Schemas, LerrynClient } from "@lerryn/api";
import {
  queryOptions,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";

import { isUnauthenticated, unwrap, useApi, useApiSession } from "../client";
import { queryKeys } from "./keys";

export type Me = Schemas["Me"];
export type Discovery = Schemas["Discovery"];
export type AccessToken = Schemas["AccessToken"];
export type AccessTokenPage = Schemas["AccessTokenPage"];
export type CreatedToken = Schemas["CreatedToken"];
export type CreateTokenRequest = Schemas["CreateTokenRequest"];
export type LoginRequest = Schemas["LoginRequest"];
export type RegisterRequest = Schemas["RegisterRequest"];

/** Server version, capabilities and who is calling. Works signed out. */
export function useDiscovery() {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.discover,
    queryFn: () => unwrap(api.GET("/api/v1/discover")),
    staleTime: 60_000,
  });
}

/**
 * Whether the deployment has no account yet, so the next sign-up becomes the
 * admin (register page tag "首个账号"). Read from discover's capabilities.
 */
export function useFirstAccountMode() {
  const discovery = useDiscovery();
  return {
    data: discovery.data?.capabilities.first_account,
    isPending: discovery.isPending,
    isError: discovery.isError,
  };
}

/**
 * Query options for the signed-in user, or null when signed out. A missing
 * session is a normal state for the console, not an error. Exported so a
 * router can load it before rendering a guarded page.
 */
export function meQuery(api: LerrynClient) {
  return queryOptions({
    queryKey: queryKeys.me,
    queryFn: async (): Promise<Me | null> => {
      try {
        return await unwrap(api.GET("/api/v1/me"));
      } catch (err) {
        if (isUnauthenticated(err)) return null;
        throw err;
      }
    },
    staleTime: 30_000,
  });
}

/** The signed-in user, or null when signed out. */
export function useMe() {
  return useQuery(meQuery(useApi()));
}

function useSignedIn() {
  const session = useApiSession();
  const queryClient = useQueryClient();
  return (me: Me) => {
    session.renew();
    // A previous getMe may still be in flight when authentication succeeds.
    void queryClient.cancelQueries();
    // Everything cached so far was read as someone else, or anonymously.
    queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== queryKeys.me[0] });
    queryClient.setQueryData(queryKeys.me, me);
  };
}

export function useLogin() {
  const session = useApiSession();
  const api = session.client;
  const signedIn = useSignedIn();
  return useMutation({
    mutationFn: (body: LoginRequest) => unwrap(api.POST("/api/v1/auth/login", { body })),
    onMutate: session.capture,
    onSuccess: (me, _body, isCurrent) => { if (isCurrent?.()) signedIn(me); },
  });
}

export function useRegister() {
  const session = useApiSession();
  const api = session.client;
  const signedIn = useSignedIn();
  return useMutation({
    mutationFn: (body: RegisterRequest) => unwrap(api.POST("/api/v1/auth/register", { body })),
    onMutate: session.capture,
    onSuccess: (me, _body, isCurrent) => { if (isCurrent?.()) signedIn(me); },
  });
}

export function useLogout() {
  const session = useApiSession();
  const api = session.client;
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/logout")),
    onMutate: session.capture,
    onSettled: (_data, _error, _variables, isCurrent) => {
      if (!isCurrent?.()) return;
      session.renew();
      queryClient.clear();
      queryClient.setQueryData(queryKeys.me, null);
    },
  });
}

const TOKEN_PAGE = 50;

/** The user's access tokens, newest first, including revoked and expired ones. */
export function useAccessTokens() {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: queryKeys.tokens,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/v1/me/tokens", {
          params: { query: { limit: TOKEN_PAGE, ...(pageParam ? { cursor: pageParam } : {}) } },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<AccessTokenPage, string | null>) => ({
      items: data.pages.flatMap((p) => p.items),
      hasMore: data.pages.at(-1)?.next_cursor != null,
    }),
  });
}

/**
 * Creates a token. The plaintext exists only in this mutation's result: it
 * is not written to the query cache or browser storage, and gcTime 0 drops
 * it from memory once the dialog that shows it calls reset() or unmounts.
 */
export function useCreateAccessToken() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    gcTime: 0,
    mutationFn: (body: CreateTokenRequest) => unwrap(api.POST("/api/v1/me/tokens", { body })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.tokens }),
  });
}

export function useRevokeAccessToken() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (tokenId: string) =>
      unwrap(api.DELETE("/api/v1/me/tokens/{tokenId}", { params: { path: { tokenId } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.tokens }),
  });
}
