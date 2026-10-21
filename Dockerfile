FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o gateway .

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=builder /app/gateway /gateway
EXPOSE 8030
ENTRYPOINT ["/gateway"]
