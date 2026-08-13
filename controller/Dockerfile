FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o aegis-controller ./cmd

FROM gcr.io/distroless/static-debian12
COPY --from=builder /app/aegis-controller /aegis-controller
ENTRYPOINT ["/aegis-controller"]
