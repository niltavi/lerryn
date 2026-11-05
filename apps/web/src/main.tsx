import "./styles.css";

import { ApiProvider, I18nProvider, StreamProvider, createConsoleClient, eventSourceStream, isApiError } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { createAppRouter } from "./router";

const api = createConsoleClient();
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 4xx answers will not change on retry; network blips and 5xx might.
      retry: (count, err) => count < 2 && !(isApiError(err) && err.status >= 400 && err.status < 500),
      refetchOnWindowFocus: true,
    },
  },
});
const router = createAppRouter({ api, queryClient });
// Same origin: EventSource sends the session cookie and resumes with Last-Event-ID.
const openStream = typeof EventSource === "undefined" ? null : eventSourceStream();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <LerrynThemeProvider>
      <I18nProvider>
        <QueryClientProvider client={queryClient}>
          <ApiProvider client={api}>
            <StreamProvider open={openStream}>
              <RouterProvider router={router} />
            </StreamProvider>
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>
    </LerrynThemeProvider>
  </StrictMode>,
);
