# Go Backend Code Quality and Architecture Standards

## 1. Architecture & Design

### Dependency Injection
You should use dependency injection to decouple components. The pattern is flexible—manual constructor injection, wire, or other approaches are acceptable as long as dependencies are explicitly declared and testable. Avoid global state and package-level singletons that make testing difficult.

### Package Structure
Follow the standard Go layout: `cmd/`, `internal/`, and `pkg/` directories.

- `cmd/`: Entry points for executables (main packages). Each service/tool gets its own subdirectory.
- `internal/`: Private application code not meant for external consumption. Most business logic lives here.
- `pkg/`: Reusable libraries safe for external import. Use sparingly and only for code with stable, well-defined APIs.

Within `internal/`, organize by domain rather than technical layer. Group related functionality together (e.g., `internal/orders/`, `internal/payments/`) rather than `internal/handlers/`, `internal/models/`, etc.

### Business Logic Decoupling Through Interfaces
Core business logic should be decoupled from infrastructure, HTTP handlers, and other concerns through interface-based design.

- Define interfaces at the point of use in business logic. The business logic package should depend on thin interfaces it needs, not on concrete implementations.
- Infrastructure (database, HTTP clients, message queues) should implement these interfaces. This makes business logic portable and testable.
- Avoid tight coupling to frameworks, transport mechanisms, or external APIs.

### Error Handling
Wrap errors with context using `fmt.Errorf` with the `%w` verb. This preserves the error chain for debugging while adding semantic information at each layer.

- Wrap errors at boundaries (infrastructure calls, user input validation, significant decision points).
- Use `errors.Is()` and `errors.As()` when checking error types in callers.
- Avoid creating custom error types unless the caller needs to distinguish specific error conditions. Simple wrapped errors are sufficient for most cases.
- Error messages should answer "what went wrong here" not just repeat the underlying error.

---

## 2. Testing Standards

### Unit Tests

#### Test File Organization
Place test files in the same package and directory as the code they test, using the `_test.go` suffix. This keeps tests close to the code and accessible for both exported and unexported functions.

#### Test Patterns
Employ table-driven tests for scenarios with multiple inputs/outputs. Table-driven tests are maintainable, scalable, and reduce boilerplate.

- Structure tables with clear case names and input/expected output columns.
- Use subtests (`t.Run()`) to run each case with better failure reporting.
- Keep test cases focused—each case should test one logical scenario.

#### Mocking Strategy
Use mocks (test doubles that replace dependencies) rather than fakes or stubs for unit testing. Mocks verify that business logic calls dependencies correctly.

- Mock interfaces defined in your business logic, not concrete types.
- Use simple, table-driven mock implementations or a mocking library (e.g., `github.com/stretchr/testify/mock`).
- Avoid over-mocking; mock only external dependencies and interfaces your code depends on.

#### Coverage Target
Unit tests should achieve >80% code coverage. This threshold catches most logic errors without requiring exhaustive testing of every code path.

- Measure coverage with `go test -cover` and track trends.
- Coverage should reflect meaningful test scenarios, not just line counting. A few tests covering critical paths are better than tests hitting lines without asserting behavior.

### Integration Tests

#### Scope
Integration tests verify that multiple components work together. These are slower than unit tests but catch issues that unit tests miss (e.g., database interactions, API contracts).

- Place integration tests in the same package, but use build tags to separate them from unit tests (e.g., `//go:build integration`).
- Run unit tests in CI by default; integration tests can run separately or on demand.

#### Test Environment Setup
Use containers (Docker) or in-memory test doubles (e.g., in-memory databases) for external dependencies.

- Keep integration tests repeatable and isolated—each test should be independent.
- Clean up resources (databases, connections) after each test.

#### Focus
Integration tests should verify:
- End-to-end request flows through handlers, business logic, and persistence.
- Correct behavior at service boundaries (API responses, error handling).
- Data consistency across multiple operations.

---

## 3. OpenTelemetry Instrumentation

### Decorator/Middleware Pattern
Instrumentation should be added through decorators or middleware layers, not embedded in business logic code. Core business logic remains clean and focused on domain problems.

- For HTTP handlers and gRPC interceptors, add instrumentation at the transport boundary.
- For business logic, wrap components at dependency injection time or use decorator functions.
- This separation keeps business logic testable without requiring instrumentation configuration.

### Signal Priority and Strategy

#### Metrics (Primary)
Metrics should measure the health and behavior of your service. Prioritize these:

- **Request metrics**: Latency (histograms), throughput (counters), error rates (counters).
- **Business metrics**: Key business operations (e.g., "orders processed", "payment failures", "items sold").
- **Resource metrics**: Database connection pool usage, queue depth, cache hit rates.

Use counters for cumulative values (requests, errors), histograms for distributions (latencies), and gauges for point-in-time values (active connections).

#### Logs (Primary)
Structured logging provides operational visibility. Log at boundaries and for significant events:

- Incoming requests and outgoing responses (at the handler/middleware level).
- Business-critical decisions and state transitions.
- Errors with sufficient context (operation, identifiers, values).
- Use structured fields (e.g., `slog`) rather than string formatting for machine-readable logs.

#### Traces (Secondary)
Traces show request flow through the system. Use them lightly for cross-service tracing and request correlation:

- Propagate trace context across service boundaries using standard propagators.
- Sample traces intelligently (not every request) to manage overhead.
- Use traces to connect logs and metrics for a single request across services.

### Implementation Patterns

- **HTTP Middleware**: Wrap handlers to record requests, responses, latencies, and errors. Extract context from headers for trace propagation.
- **gRPC Interceptors**: Use unary and stream interceptors to observe calls, measure latencies, and count errors.
- **Database Decorators**: Wrap query execution to measure database latency and error rates.
- **Context Propagation**: Pass `context.Context` through all call chains so instrumentation can access request-scoped data (trace IDs, user IDs, etc.).

---

## 4. API Design

### REST APIs

#### Structure
REST APIs should expose domain resources through standard HTTP methods and status codes.

- Use nouns for resources (e.g., `/orders`, `/payments`), not verbs.
- Use HTTP methods meaningfully: GET (retrieve), POST (create), PUT/PATCH (update), DELETE (remove).
- Return appropriate status codes: 2xx for success, 4xx for client errors, 5xx for server errors.

#### Error Responses
REST APIs should return meaningful error information, not just a status code.

- Return a structured error response (e.g., JSON) with:
  - A descriptive message explaining what went wrong.
  - An error code or type for programmatic handling.
  - Relevant context (e.g., which field failed validation, why a resource wasn't found).
- Example structure: `{ "error": "validation_failed", "message": "...", "details": {...} }`.
- Avoid exposing internal errors (stack traces, database details) in responses. Log these internally.

#### Versioning
Version APIs in the URL path (e.g., `/api/v1/`) to manage breaking changes gracefully.

### gRPC Services

#### Structure
gRPC services should reflect domain operations with clear request/response messages.

- Use `.proto` files to define services, and generate Go code.
- Organize services by domain (e.g., `ordersvc.OrderService`, `paymentsvc.PaymentService`).
- Use standard RPC naming: method names describe actions (Get, List, Create, Update, Delete, etc.).

#### Error Handling
gRPC uses status codes and error details to communicate failures.

- Return appropriate `google.rpc.Code` statuses (e.g., `NOT_FOUND`, `INVALID_ARGUMENT`, `INTERNAL`).
- Attach error details using `google.rpc.Status` to provide context (validation errors, resource identifiers, retry info).
- Avoid exposing sensitive information in error messages.

#### Streaming
When using streaming (client-side, server-side, or bidirectional), ensure proper error handling and resource cleanup.

- Close streams cleanly and propagate errors through the stream's error return.
- Use context cancellation to terminate streams on client request or timeout.

---

## 5. Code Style

### Effective Go
Go code should follow the official [Effective Go](https://golang.org/doc/effective_go) guidelines. Key principles:

- **Naming**: Use clear, concise names. Functions and variables should be self-documenting.
- **Interfaces**: Define interfaces where behavior is needed, not where it's provided. Keep interfaces small.
- **Concurrency**: Use goroutines and channels idiomatically. Avoid overcomplicating concurrency; simple synchronization often suffices.
- **Error handling**: Explicit error checking is preferred over exceptions. Return errors as values.
- **Comments**: Comment exported functions, types, and packages. Avoid obvious comments; explain *why*, not *what*.

### Go Idioms
Code should feel idiomatic and natural to Go developers:

- Prefer composition over inheritance.
- Avoid nil pointers and the null object pattern; use explicit optional types or zero values.
- Use `defer` for cleanup (closing resources, unlocking mutexes).
- Employ the blank identifier (`_`) to ignore unused values.
- Return multiple values for error handling rather than exceptions.

### Code Organization
- One type per file unless types are closely related. Keep files focused.
- Initialize structs with descriptive field names; avoid positional constructor arguments for complex types.
- Prefer simple implementations; avoid unnecessary abstraction.

---

## 6. Integration: Bringing It Together

### Example Workflow
A typical service should:

1. **Handle a request** at a transport boundary (HTTP handler, gRPC method).
2. **Extract and validate** input, returning a meaningful error if invalid.
3. **Call business logic** (functions or methods on domain types).
4. **Record metrics and logs** via middleware/decorators, not in business logic.
5. **Return a response** with appropriate status code and structure.

Business logic should never import transport, database, or instrumentation packages. It should depend only on interfaces it defines for external concerns.

### Testing Pyramid
- **Unit tests** (many): Fast, focused on business logic with mocked dependencies.
- **Integration tests** (some): Verify components work together with real or in-memory infrastructure.
- **End-to-end tests** (few): Test full service flows in staging or production-like environments.

---

## 7. Checklist for New Code

When writing a new service or component, verify:

- [ ] Business logic is decoupled through interfaces.
- [ ] Package structure follows `cmd/internal/pkg` layout.
- [ ] Errors are wrapped with context using `fmt.Errorf("%w", ...)`.
- [ ] Unit tests use table-driven patterns with mocks, achieving >80% coverage.
- [ ] Integration tests exercise service boundaries and data interactions.
- [ ] Instrumentation (metrics, logs, traces) is applied via middleware/decorators, not in business logic.
- [ ] REST APIs return meaningful error responses; gRPC services use proper status codes and details.
- [ ] Code follows Effective Go and idiomatic Go principles.
- [ ] Exported functions and types have doc comments.
