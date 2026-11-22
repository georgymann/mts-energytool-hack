FROM golang:1.26-alpine@sha256:30b1e546a9c7fa3b747c2bdba3a0c7bdf3a02ebb4c5b8405d2f948d7e8aac4ec AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/energytool-api ./cmd/api

FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f

RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/energytool-api /app/energytool-api
RUN mkdir -p /app/var /app/knowledge && chown -R app:app /app
USER app
ENV BACKEND_ADDR=:8080 BACKEND_STORE_PATH=/app/var/sessions.json
EXPOSE 8080
ENTRYPOINT ["/app/energytool-api"]