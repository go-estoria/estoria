# estoria

[![Go Report Card](https://goreportcard.com/badge/github.com/go-estoria/estoria)](https://goreportcard.com/report/github.com/go-estoria/estoria)

Estoria is an event sourcing toolkit for Go.

>**Note**: This project is in early beta. While functional, the API is not yet stable and is not suitable for production use.

Event sourcing enables you to model your application as a series of state-changing events. This approach can make it easier to reason about your application's behavior, as well as to implement features like auditing, replay, and time travel.

Estoria provides composable components for implementing event sourcing in a Go application, including:

- Event-based aggregate state management
- Flexible event store implementations
- Transactional outbox processing
- Aggregate snapshotting
- Aggregate caching
- Lifecycle hooks

## Getting Started

```shell
go get github.com/go-estoria/estoria
```

See [estoria-examples](https://github.com/go-estoria/estoria-examples) for runnable API usage examples.

## Component Providers

See [estoria-contrib](https://github.com/go-estoria/estoria-contrib) for officially-supported event store, snapshot store, and aggregate cache implementations.
