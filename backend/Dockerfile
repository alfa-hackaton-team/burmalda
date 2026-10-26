FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -o /out/userctl ./cmd/userctl && \
    CGO_ENABLED=0 go build -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -o /out/observer ./cmd/observer && \
    CGO_ENABLED=0 go build -o /out/retention ./cmd/retention && \
    CGO_ENABLED=0 go build -o /out/rawstorage ./cmd/rawstorage && \
    CGO_ENABLED=0 go build -o /out/dataimport ./cmd/dataimport && \
    CGO_ENABLED=0 go build -o /out/sourcectl ./cmd/sourcectl && \
    CGO_ENABLED=0 go build -o /out/mlctl ./cmd/mlctl && \
    CGO_ENABLED=0 go build -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
COPY config.example.yaml /app/config.yaml
COPY examples/sources/ /app/examples/sources/
WORKDIR /app
