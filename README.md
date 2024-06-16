# estoria

Estoria is an event sourcing toolkit for Go.

>**Note: This project is in its alpha phase, the API changes frequently, and it is not yet ready for production use.**

See [Getting Started](#getting-started) to start using Estoria.

## V1 Roadmap (subject to change)

- [~] Features
  - [X] Aggregate Store
    - [X] Event-Sourced (Core)
    - [X] Cached
    - [X] Snapshotting
    - [X] Hookable
  - [~] Event Store Implementations
    - [X] In-memory
    - [~] Persistent (via [estoria-contrib](https://github.com/go-estoria/estoria-contrib))
      - [X] EventStoreDB
      - [X] Postgres
      - [X] MongoDB
      - [ ] DynamoDB
      - [ ] MySQL
      - [ ] Azure Cosmos DB
      - [ ] Google Cloud Spanner
      - [ ] SQLite
      - [?] Cassandra (uncommitted)
      - [?] Google Cloud Firestore (uncommitted)
      - [?] MSSQL (uncommitted)
  - [ ] Outbox Processing
  - [X] Event-sourced Aggregate Store
- [ ] Tests
  - [ ] Unit Tests
  - [ ] Integration Tests
- [ ] Documentation
  - [ ] README
  - [ ] GoDoc
  - [ ] Examples
- [ ] Examples
  - [ ] Basic Usage
  - [ ] Caching
  - [ ] Lifecycle Hooks
  - [ ] Outbox Processing

## Getting Started

```shell
go get github.com/go-estoria/estoria
```

See the [example project in estoria-contrib](https://github.com/go-estoria/estoria-contrib/tree/main/example) for a complete example.

## Event Store Providers

See [estoria-contrib](https://github.com/go-estoria/estoria-contrib) for officially-supported event store implementations.

## License

This project is licensed under the MIT License.
