# Use the official Golang image as the base image
FROM golang:1.27-alpine AS builder

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code
COPY . ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o clickhouse-schemaflow-visualizer .

# Use a minimal alpine image for the final image
FROM alpine:3.22

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Set the working directory
WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/clickhouse-schemaflow-visualizer .

# Copy the frontend files
COPY static/ ./static/

# Drop root. The app binds 8080, needs no privileged port and writes nothing to
# disk, so it has no reason to run as uid 0. Mirrors the systemd unit the .deb
# and .rpm packages install, which has always run under its own account.
RUN addgroup -S -g 10001 schemaflow \
    && adduser -S -u 10001 -G schemaflow -H -D schemaflow
USER 10001:10001

# Expose the port
EXPOSE 8080

# Run the application
CMD ["./clickhouse-schemaflow-visualizer"]
