# IdeaLode — web arayüzü binary'si (ui modülü, #178). backend'i import
# ETMEZ; API_BASE_URL üzerinden apiclient ile konuşur, DATABASE_URL görmez.
# Railway'de bu servisin Root Directory'si ui/ olmalı — kökteki railway.json
# ve Dockerfile'ı görmemesi için (#178 düzeltmesi, bkz. ui/railway.json).

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /app/web ./cmd/web

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /app/web /app/web
ENTRYPOINT ["/app/web"]
