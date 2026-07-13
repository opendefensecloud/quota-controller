FROM --platform=$BUILDPLATFORM golang:1.26.5@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS builder

WORKDIR /workspace
RUN go env -w GOMODCACHE=/root/.cache/go-build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH

RUN mkdir bin

FROM builder AS controller-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o bin/quota-controller ./cmd/controller/

FROM builder AS webhook-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o bin/quota-webhook ./cmd/webhook/

FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240 AS controller
WORKDIR /
COPY --from=controller-builder /workspace/bin/quota-controller .
USER 65532:65532
ENTRYPOINT ["/quota-controller"]

FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240 AS webhook
WORKDIR /
COPY --from=webhook-builder /workspace/bin/quota-webhook .
USER 65532:65532
ENTRYPOINT ["/quota-webhook"]

# Combined image with both binaries (default target; used by the Helm chart — the
# controller Deployment overrides command to /quota-controller and the webhook
# Deployment overrides to /quota-webhook).
FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240
WORKDIR /
COPY --from=controller-builder /workspace/bin/quota-controller .
COPY --from=webhook-builder /workspace/bin/quota-webhook .
USER 65532:65532
# Fallback for standalone runs; Helm Deployments override this via command:.
CMD ["/quota-controller"]
