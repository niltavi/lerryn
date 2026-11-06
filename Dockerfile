# syntax=docker/dockerfile:1

# The web console, built once on the build platform; its output is
# architecture-independent and gets embedded into the binary below.
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web
WORKDIR /src
ENV PNPM_HOME=/pnpm COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
# Only the manifests of the design packages: the lockfile lists them as
# workspace projects, but the console needs none of their dependencies.
COPY design/ design/
COPY packages/ packages/
COPY apps/web/ apps/web/
# The Tools page bundles both integration guides as downloadable assets.
COPY docs/zh-CN/tools.md docs/zh-CN/
COPY docs/en/tools.md docs/en/
RUN --mount=type=cache,id=pnpm,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store \
    && pnpm install --frozen-lockfile --filter @lerryn/web...
RUN pnpm --filter @lerryn/web build

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# The server embeds the bundled OpenAPI contract from api/, the sbx SDK
# that runners mount into task containers from runtimes/sdk/, the agent
# skills from skills/, the logo the credential fill page shows from
# design/brand/, and the web console built above.
COPY api/ api/
COPY runtimes/sdk/ runtimes/sdk/
COPY skills/ skills/
COPY design/brand/brand.go design/brand/
COPY design/brand/logo/svg/lerryn-mark.svg design/brand/logo/svg/
COPY server/ server/
COPY --from=web /src/apps/web/dist/ server/internal/console/webdist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w \
        -X github.com/niltavi/lerryn/server/internal/buildinfo.Version=${VERSION} \
        -X github.com/niltavi/lerryn/server/internal/buildinfo.Commit=${COMMIT} \
        -X github.com/niltavi/lerryn/server/internal/buildinfo.Date=${DATE}" \
      -o /out/lerryn ./server/cmd/lerryn \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
LABEL org.opencontainers.image.title="lerryn" \
      org.opencontainers.image.source="https://github.com/niltavi/lerryn" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"
COPY --from=build /out/lerryn /usr/local/bin/lerryn
# The license and the notices of everything linked into the binary travel
# with the image (AGPL-3.0 and the third-party licenses require it).
COPY LICENSE THIRD_PARTY_NOTICES /usr/share/doc/lerryn/
# Default data directory, writable by nonroot. For all-in-one, bind-mount a
# host directory at the same path inside and outside the container instead
# of a named volume: task containers mount subdirectories of it through the
# host daemon (tech design 15.2).
COPY --from=build --chown=65532:65532 /out/data /var/lib/lerryn
USER nonroot:nonroot
EXPOSE 33333
ENTRYPOINT ["/usr/local/bin/lerryn"]
CMD ["server"]
