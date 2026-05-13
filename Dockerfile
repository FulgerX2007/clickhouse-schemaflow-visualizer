# Use the official Golang image as the base image
FROM golang:1.26-alpine AS builder

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
FROM alpine:3.18

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Set the working directory
WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/clickhouse-schemaflow-visualizer .

# Copy the frontend files
COPY static/ ./static/

# Expose the port
EXPOSE 8080

# Run the application
CMD ["./clickhouse-schemaflow-visualizer"]
