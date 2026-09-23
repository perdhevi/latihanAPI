# latihanAPI

A simple Go WebServer template.

## Getting Started

### Prerequisites

- Go 1.27 or later

### Running the application

To run the server, use:

```bash
go run main.go
```

The server will start at `http://localhost:8080`.

## API Endpoints

- `GET /` - Returns a welcome message.
- `GET /health` - Health check endpoint.

## Project Structure

- `main.go` - Entry point and server configuration.
- `go.mod` - Go module definition.
