FROM golang:1.27 AS builder

ARG GITEA_MCP_REF=aaba3da5ecbd6badaa524177a29eb2ee7e8deb62

RUN git init /src \
 && git -C /src remote add origin https://gitea.com/anshmendiratta/gitea-mcp.git \
 && git -C /src fetch --depth 1 origin "$GITEA_MCP_REF" \
 && git -C /src checkout --detach FETCH_HEAD \
 && test "$(git -C /src rev-parse HEAD)" = "$GITEA_MCP_REF"

WORKDIR /src
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gitea-mcp

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder --chown=nonroot:nonroot /gitea-mcp .

USER nonroot:nonroot

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/app/gitea-mcp", "-healthcheck"]

CMD ["/app/gitea-mcp"]
