# estoria

Estoria is an event sourcing toolkit for Go.

>**Note: This project is in its alpha phase, the API changes frequently, and it is not yet ready for production use.**

See [Getting Started](#getting-started) to start using Estoria.

## V1 Roadmap (subject to change)

- [ ] Features
  - [ ] Event Store Implementations
    - [X] In-memory
    - [~] Persistent (via [estoria-contrib](https://github.com/go-estoria/estoria-contrib))
      - [ ] DynamoDB
      - [X] EventStoreDB
      - [X] Postgres
      - [ ] MySQL
      - [X] MongoDB
      - [ ] SQLite
      - [?] Azure Cosmos DB (uncommitted)
      - [?] Cassandra (uncommitted)
      - [?] Google Cloud Firestore (uncommitted)
      - [?] Kafka (uncommitted)
      - [?] MSSQL (uncommitted)
      - [?] NATS (uncommitted)
      - [?] RabbitMQ (uncommitted)
  - [ ] Outbox Processing
  - [X] Event-sourced Aggregate Store
  - [X] Aggregate Store Wrappers
    - [X] Caching
    - [X] Snapshotting
    - [X] Lifecycle Hooks
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

See [estoria-contrib](https://github.com/go-estoria/estoria-contrib) for officially-supported event store implementations.

## License

This project is licensed under the MIT License.
