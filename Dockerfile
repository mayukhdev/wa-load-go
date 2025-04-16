# Build stage
FROM golang:1.24 AS builder

WORKDIR /app

ARG TARGETARCH=amd64

# Copy go.mod and go.sum first for caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the application source code
COPY . ./

# Force a static binary to avoid missing dependencies
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o main .


# Final minimal image
FROM alpine:3.21

RUN apk add --no-cache tzdata
ENV TZ=Asia/Kolkata

WORKDIR /root

COPY --from=builder /app/main .
COPY --from=builder /app/*.ogg .

EXPOSE 8080

CMD ["./main"]
