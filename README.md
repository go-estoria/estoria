# estoria

![GitHub Tag](https://img.shields.io/github/v/tag/go-estoria/estoria)
![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/go-estoria/estoria/go-tests.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/go-estoria/estoria)](https://goreportcard.com/report/github.com/go-estoria/estoria)
[![godoc](https://camo.githubusercontent.com/eaf508ed0d0cafd481fcfae3c2c8177f42df213191153a1963a221d13f80aa21/68747470733a2f2f706b672e676f2e6465762f62616467652f6769746875622e636f6d2f5468726565446f74734c6162732f77617465726d696c6c2e737667)](https://pkg.go.dev/github.com/go-estoria/estoria)
![Documentation](https://img.shields.io/badge/-Documentation-navy)

Estoria is an event sourcing toolkit for Go.

Event sourcing enables you to model your application as a series of state-changing events. This approach can make it easier to reason about your application's behavior, as well as to implement features like auditing, replay, and time travel.

Estoria provides composable components for implementing event sourcing in a Go application, including:

- Event-based aggregate state management
- Flexible event store implementations
- Aggregate snapshotting
- Aggregate caching
- Lifecycle hooks

## Getting Started

```shell
go get github.com/go-estoria/estoria
```

See the [Getting Started](https://go-estoria.github.io) guide for an introduction to the core concepts and components.

See [estoria-examples](https://github.com/go-estoria/estoria-examples) for runnable examples using various backends.

## Component Providers

See [estoria-contrib](https://github.com/go-estoria/estoria-contrib) for officially-supported event store, snapshot store, and aggregate cache implementations.
