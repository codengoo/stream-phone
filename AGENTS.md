# Agent Rules

## Scope

- Implement only what is required for Windows.
- Do not add Linux or macOS compatibility unless explicitly requested.
- Assume the runtime environment, filesystem behavior, process execution model, and path conventions are Windows-specific.

## Module Design

- Every new module must have a single clear responsibility.
- Split new functionality into small modules when responsibilities differ, even if the feature is still simple.
- Do not create large multi-purpose modules that mix orchestration, business logic, filesystem access, process execution, parsing, and formatting in one place.
- Prefer composition of small modules over centralized modules with many branches and responsibilities.
- If a module starts growing beyond one clear concern, split it immediately.

## Boundaries

- Separate core logic from Windows integration details where practical.
- Keep OS-facing code isolated so Windows-specific behavior is explicit and easy to find.
- Put shared decision logic in focused modules; put side effects such as process spawning, filesystem access, and shell interaction in dedicated modules.

## Implementation Constraints

- Use Windows-native assumptions for paths, commands, and process handling.
- Prefer solutions that are straightforward and reliable on Windows over cross-platform abstractions.
- Do not introduce portability layers, platform switches, or abstraction for unsupported operating systems unless explicitly requested.

## Code Review Standard

- Reject any new module that has more than one primary responsibility.
- Reject any implementation that introduces unnecessary cross-platform logic.
- Reject any change that hides Windows-specific assumptions instead of making them explicit.

## Default Decision Rule

- When uncertain, choose the design that produces smaller modules and simpler Windows-only behavior.

## Documentation Sync

- `docs/api.md` is the single source of truth for the HTTP API.
- **Every change to an HTTP endpoint** (new route, removed route, changed path, changed query params, changed request or response shape) **must include a matching update to `docs/api.md` in the same commit.**
- Endpoint changes without a corresponding `docs/api.md` update must be rejected in code review.
- When adding a new capture backend, also update the `?type` table in `docs/api.md`.
- When adding new documentation files, register them in `docs/project.md`.
