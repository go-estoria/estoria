# estoria

Estoria is an event sourcing toolkit for Go.

>**Note: This project is in its alpha phase, the API changes frequently, and it is not yet ready for production use.**

See [Getting Started](#getting-started) to start using Estoria.

## Features

- Unified event store interface supporting multiple backends
- Event store wrappers for caching and lifecycle hooks
- Transactional outbox with at-least-once processing
- Event-sourced aggregate store
- Aggregate store wrappers for caching, snapshotting, and lifecycle hooks

## V1 Roadmap

- [ ] Features
  - [ ] Event Store Implementations
    - [ ] In-memory
    - [ ] SQL (non-specific, using `database/sql` only) (_maybe_)
  - [ ] Event Store Wrappers
    - [ ] Caching
    - [ ] Lifecycle Hooks
  - [ ] Outbox Processing
  - [ ] Event-sourced Aggregate Store
  - [ ] Aggregate Store Wrappers
    - [ ] Caching
    - [ ] Snapshotting
    - [ ] Lifecycle Hooks
  - [ ] Commandable Aggregates
    - [ ] Command Handlers
    - [ ] Command Bus
  - [ ] Projections
    - [ ] In-memory
    - [ ] Persistent
- [ ] Tests
  - [ ] Unit Tests
  - [ ] Integration Tests
  - [ ] E2E Tests
- [ ] Documentation
  - [ ] README
  - [ ] GoDoc
  - [ ] Examples
- [ ] Examples
  - [ ] Basic Usage
  - [ ] Caching
  - [ ] Lifecycle Hooks
  - [ ] Outbox Processing
  - [ ] Commandable Aggregates
  - [ ] Projections

## Getting Started

```shell
go get github.com/go-estoria/estoria
```

See the [example project in estoria-contrib](https://github.com/go-estoria/estoria-contrib/tree/main/example) for a complete example.

## Event Store Providers

See [estoria-contrib](https://github.com/go-estoria/estoria.contrib) for officially-supported event store implementations.

## License

This project is licensed under the MIT License.
