# Repository Instructions

## Workspace Intent

- `aaPanel/` is a local reference snapshot for product behavior, copy, layout, and implementation ideas.
- `src/` is the active mini-panel codebase that we edit.

## Rules For Codex And Other Agents

- Do not modify files under `aaPanel/` unless the user explicitly asks to edit that directory.
- Treat `aaPanel/` as read-only reference material by default.
- When implementing or adjusting a feature in `src/`, check whether the same feature or UI pattern exists in `aaPanel/` first.
- Prefer matching aaPanel behavior and wording when the user asks for aaPanel-style UI or feature parity.
- Keep all new implementation work in the mini-panel app unless the user clearly requests changes to the reference snapshot.

## Practical Workflow

- Use `aaPanel/` to inspect templates, scripts, and naming before changing `src/`.
- Copy behavior, not files: adapt the reference into the Rust mini-panel architecture instead of patching aaPanel directly.
- If aaPanel and mini-panel differ, preserve mini-panel's runtime model while aligning UI and feature behavior as closely as practical.

## aaPanel Parity Checklist

- Locate the equivalent feature, template, or script inside `aaPanel/` before editing `src/`.
- Confirm whether the user wants visual similarity, behavior parity, or both.
- Reuse aaPanel wording, labels, and information hierarchy when practical.
- Implement the change in `src/`, not in `aaPanel/`.
- Keep a note of any intentional differences caused by Rust architecture, local platform limits, or missing backend data.
