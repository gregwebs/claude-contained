# Bash

Use `set -euo pipefail`.

Be very careful with using `rm` and other destructive commands.

Prefer simple shell idioms- require a lower level of bash proficiency for reviewers.

When functions are being written, question whether shell should be used.
Functions can end up interacting poorly with `set -e`

Write tests for bash code.

# Code Quality Guidelines

# Testing

Look at the /tdd skill for further testing guidance.

Use property based testing, including smart quickcheck style tests.

Use contracts. If there is not automated contract testing, state contracts as comments in functions.

## DRY

Follow the DRY (Don't Repeat Yourself) Principle and Avoid Duplicating Code or Logic.
Avoid writing the same code more than once. Instead, reuse your code using functions, classes, modules, libraries, or other abstractions.

* ALWAYS maintain a single source of truth for configuration values — the same constant or config value defined in two places will diverge and cause bugs.
* ALWAYS prefer readability over DRY when the abstraction requires indirection that obscures what the code does — a small amount of duplication is often better than an obscure helper.
* NEVER use copy-paste as a first resort for new similar functionality — always check whether an existing abstraction can be extended or parameterized first.

Caution:
* Dont' quickly apply DRY to coincidentally similar code that serves different purposes. Unrelated concepts should not be coupled. In this case DRY should require a function that is generic with respect to either concern.
* It is okay to wait to use a shared abstraction until you have 3 concrete instances of the same logic if the value initially appears low for combining just 2 instances.

## Constants

Define thresholds and parameters as named constants.

## Types

Use strong typing. Make invalid states unrepresentable with types. Represent the problem domain properly with types. Use enums and case analysis.

Examples:
* After a string is validated/parsed it should be given a new type.
* Don't use dynamic types (e.g. as `any` in Go). Use generic types. If using a library that uses dynamic types, convert them to strong types as quickly as possible.
* Don't use the same type multiple times in a row for function arguments (these could get confused). Use a newtype for one of the arguments or use named arguments (via a struct or other language construct) for some of the function arguments.
* The callee should use validated types that the caller should produce.


## Error Handling

Always handle errors. Logging an error at the error site is equivalent to ignoring it. An error should be passed up callers until it reaches an error handler that can deal with it. Catch all error handlers should only exist at a top level and must properly handle the error by terminating the program in an exit state or returning an error code after logging the error.

## Tracing

We should be able to roughly understand what is going on in a program by looking at the logs.
Add lots of debug level logging statements.
Info level statements should show what is happening in the program at a high level.
Programs should be able to set the log level via an environment variable or CLI.
Use log sections- metadata indicating what part of the codebase is being exercised.
Debug logs should be filterable to a log section.

## Testing

Refactor code into small testable functions. Write lots of tests without using mocks.
Write seeds for creating data that is needed for testing.

## Documentation

Comments state design constraints, invariants, and **why**. Not **what** the code does. If you feel a comment is needed to restate what the next line does, that almost always means the code should be refactored to make what it is doing easier to understand. For example extract a function with a descriptive name.

# Go

## Error handling

Always handle errors. Logging errors is not handling them. There are few exceptions:
* deferrerd closing of resources- it is often safe to log the error at a warning level without returning it.
* some APIs return errors for expected (non-error) outcomes (for example EOF). We need to make sure we check the error type and handle the other errors

Avoid using panic/recover if possible.
In stateless servers, a panic for one request should not crash the program but should be reported as an error.

If a for loop has multiple continue statements where errors are collected, consider refactoring to call a function and have just one continue statement.

## HTTP
 
* Client retries: use failsafe Go [github.com/failsafe-go/failsafe-go](https://failsafe-go.dev/http/)
* Always check the HTTP error code
* Log requests and responses when there is an HTTP error unless the information is sensitive and there are no redaction facilities

## CLI options

Use github.com/alexflint/go-arg for CLI argument parsing.
If the options are very simple, the standard library flags can be used.

## Newtype pattern

Instead of using raw Golang types, we should often use a new type.
Parse, don't validate, and help ensure that with a simple new type.

```go
type ProductName string
```

## Linting

Write code that passes standard linters. Use `.golangci.yml`.

## Nesting

Avoid nesting code; keep the "happy path" to the left

### Context

Accept `context.Context` as first parameter in functions that perform I/O or long-running operations.
Do not store contexts in structs or pass a nil context.
Avoid storing values in the context unless the value should be (request) scoped for context usage.

### Logging

Use structured logging with slog.
Never log sensitive data- sanitize before logging or placing into errors.
The user should be able to specify a log level via a CLI option or env variable.

### Testing

Use `t.Helper()` to improve failure reporting.
avoid `time.Now()` and other non-deterministic testing.

### Documentation

When writing examples, use an `Example` function instead of doc comments.

# Security

Look for security vulnerabilities in newly written code so that we can fix them before the code is committed to main.
Below is a non-exhaustive list of reminders/preferences around certain security practices.

## Secret handling

Use env vars or secure config management for sensitive data. Never put literal secrets in code.
Secrets should only be stored encrypted. They should be encrypted in transit (HTTPS).

## Data exfiltration and redaction

Code that can touch production data should have restricted network access.
Production data should be redacted before being printed, logged, or otherwise leaving the code.

## Defense in depth

Consider how to add security at different boundaries and interfaces.

## Boundaries

Inputs, particularly dirctly from end users or untrused clients should be validated.
Use parameterized queries for database access.
Escape special characters in user-generated content before rendering it in HTML.
When generating output contexts such as HTML or SQL, use safe frameworks or encoding functions to avoid vulnerabilities. 

Always include appropriate security headers (Content Security Policy, X-Frame-Options, etc.) in web responses, and use frameworks’ built-in protections for cookies and sessions.

## Libraries

Evaluate the security posture of libraries before using them.
Consider whether we can use a small custom implementation of a few functions rather than a large library.
Prefer high-level libraries for cryptography rather than rolling your own.

Generate a Software Bill of Materials (SBOM) by using tools that support standard formats like SPDX or CycloneDX.

## Infrastructure

When adding important external resources (scripts, containers, etc.), include steps to verify integrity (like checksum verification or signature validation) if applicable. If running as a service, drop privileges when possible. When using containers, use minimal base images and avoid running containers with the root user.

## Version pinning

Pin dependency versions to immutable digests. Verify signatures.
