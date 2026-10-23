FROM golang:1.27 AS builder

ARG GITEA_MCP_REF=aaba3da5ecbd6badaa524177a29eb2ee7e8deb62

RUN git clone --depth 1 --branch feat/create-organization https://gitea.com/anshmendiratta/gitea-mcp.git /src \
 && test "$(git -C /src rev-parse HEAD)" = "$GITEA_MCP_REF"

WORKDIR /src
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gitea-mcp

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder --chown=nonroot:nonroot /gitea-mcp .

USER nonroot:nonroot

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/app/gitea-mcp", "-healthcheck"] || exit 1

CMD ["/app/gitea-mcp"]
