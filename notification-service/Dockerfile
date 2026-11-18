FROM golang:1.25.10-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY notification-service ./notification-service
RUN CGO_ENABLED=0 go build -trimpath -o /app ./notification-service

FROM scratch
COPY --from=build /app /app
USER 65532:65532
ENTRYPOINT ["/app"]
