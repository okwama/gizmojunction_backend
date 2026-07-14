# gizmojunction_backend

Go-based backend API service for Gizmo Junction.

## Project Structure

* `cmd/` - Entry points for the application (e.g., `cmd/api/main.go`).
* `internal/` - Private application and business logic (handlers, services, repositories).
* `migrations/` - Database schema migration files.

## Getting Started

### Prerequisites
* Go 1.22+
* PostgreSQL / MySQL (or your database of choice)

### Running the API
```bash
go run cmd/api/main.go