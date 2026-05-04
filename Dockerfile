FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/txmill ./cmd/txmill && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/txmill  /usr/local/bin/txmill
COPY --from=builder /out/migrate /usr/local/bin/migrate
USER 0:0
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/txmill"]
