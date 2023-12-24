# continuum

## Motivations

- Provide adequate abstractions at every layer to allow for hyper customization and extensibility
- Assume nothing about storage backends
- Provide sensible defaults and factories for common implementations
- Ability for individual entities to be event-sourced, rather than the entire app's data
- Event-sourcing as a storage abstraction below the application level

## Feature Brainstorming

- Diffing of entities to auto-generate events?!?
- Middleware
  - Event appending
  - Event application (upgrading events, etc)
  - Tracing: OTEL, etc
- CQRS components?
- Outbox functionality
- Event bus(es)
- CLI tool for generating commands/events for existing entity types?
