# Coding standards

The rules a human or an agent holds in their head while writing and reviewing
code here. Each rule's essence is here; the longer reasoning lives in the doc it
links.

## Code ordering

A file should tell a readable story from top to bottom. When helpers, constants, and types are scattered randomly, the reader — human or agent — is forced to untangle implementation details before understanding what the module actually does. Follow a predictable vertical structure so anyone opening the file knows exactly where to look.

### The top-to-bottom flow

Every file must follow this general order, omitting any steps that don't apply to the language:

1. **Module setup:** File-level docstrings, package declarations, or compiler pragmas.
2. **Imports:** Grouped and sorted. Standard library first, third-party packages second, and internal/local paths last.
3. **Module-level Constants:** Magic numbers, environment variable keys, and static configurations. Keep them together before any execution logic.
4. **Types and Data Structures:** The interfaces, structs, or models that define the domain shapes used or returned by the module.
5. **The Public API (Main logic):** The primary exported class, main function, or core services. This is what the caller cares about; put it as high as possible.
6. **Internal Helpers:** Private utilities and implementation details that support the main logic.

### Public first, private last

Keep the entry points high, and push the internal details down. Whenever the compiler or interpreter allows it, place helper functions at the very bottom of the file.

If the language strictly requires defining a function before it is called, group all internal helpers together neatly just above the main logic. Never interleave them. Do not write a public function, followed by its private helper, followed by another public function. Group the public interface together, and group the private implementation together.

## Agents

### How to approach decisions and uncertainties

For each option (grilling, design or implementation), provide a brief summary: what it does, what it affects, and why it is a good choice. Avoid unexplained jargon and repetition.
