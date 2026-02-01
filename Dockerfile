# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o cv-search-api cmd/api/main.go

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates poppler-utils

WORKDIR /root/

# Copy binary from builder
COPY --from=builder /app/cv-search-api .
COPY --from=builder /app/docs ./docs

# Create uploads directory
RUN mkdir -p /root/uploads

# Expose port
EXPOSE 8080

# Run
CMD ["./cv-search-api"]
