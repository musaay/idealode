# IdeaLode — pipeline/API binary'si (backend modülü). Monorepo'da #178'den
# beri iki ayrı Go modülü var (backend/, ui/); Nixpacks'ın kök dizin
# varsayımına güvenmek yerine deterministik bir multi-stage build kullanıyoruz.
# ui/ bu imaja hiç girmez (bkz. .dockerignore + Dockerfile.ui).

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 go build -trimpath -o /app/idealode ./cmd/idealode

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /app/idealode /app/idealode
ENTRYPOINT ["/app/idealode"]
CMD ["run"]
