# estoria

![GitHub Release](https://img.shields.io/github/v/release/go-estoria/estoria?color=0000FF00)
![GitHub Tag](https://img.shields.io/github/v/tag/go-estoria/estoria)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/go-estoria/estoria)
![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/go-estoria/estoria/ci.yml)
[![godoc](https://pkg.go.dev/badge/github.com/go-estoria/estoria.svg)](https://pkg.go.dev/github.com/go-estoria/estoria)
[![Documentation](https://img.shields.io/badge/-Documentation-navy)](https://estoria.dev)

Estoria is an event sourcing toolkit for Go.

Event sourcing enables you to model your application as a series of state-changing events. This approach can make it easier to reason about your application's behavior, as well as to implement features like auditing, replay, and time travel.

Estoria provides composable components for implementing event sourcing in a Go application, including:

- Event-based aggregate state management
- Flexible event store implementations
- Per-event metadata and declared payload content types
- Aggregate snapshotting and caching
- Global event reads for building read models and projections
- Stream deletion and snapshot retention
- Lifecycle hooks
- Acceptance test suites for third-party backend implementations

## Getting Started

Estoria requires Go 1.26 or later.

```shell
go get github.com/go-estoria/estoria
```

See the [Getting Started](https://estoria.dev) guide for an introduction to the core concepts and components.

See [estoria-examples](https://github.com/go-estoria/estoria-examples) for runnable examples using various backends.

## Component Providers

See [estoria-contrib](https://github.com/go-estoria/estoria-contrib) for officially-supported event store, snapshot store, and aggregate cache implementations.
