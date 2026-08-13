# IdeaLode — pipeline binary. Monorepo'da Go modülü api/ altında olduğu için
# Nixpacks'ın kök dizin varsayımına güvenmek yerine deterministik bir
# multi-stage build kullanıyoruz.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY api/go.mod api/go.sum ./
RUN go mod download
COPY api/ .
RUN CGO_ENABLED=0 go build -trimpath -o /app/idealode ./cmd/idealode

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /app/idealode /app/idealode
ENTRYPOINT ["/app/idealode"]
CMD ["run"]
