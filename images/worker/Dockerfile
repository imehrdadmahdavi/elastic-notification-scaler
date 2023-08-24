# Start from the official Golang base image
FROM golang:latest AS builder

# Set the current working directory inside the container
WORKDIR /app

# Copy main.go
COPY main.go /app/

# Initialize a new module and download dependencies
RUN go mod init worker
RUN go mod tidy
RUN go mod download

# Build the Go app
RUN CGO_ENABLED=0 GOOS=linux go build -o worker main.go

# Start a new stage from scratch
FROM alpine:latest

# Copy the binary from builder to the current stage
COPY --from=builder /app/worker /worker

# Command to run the binary
ENTRYPOINT ["/worker"]
