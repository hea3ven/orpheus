# Documentation instructions

Orpheus is an unreleased, single-user MVP. Document the implemented workflow, not a released API or a compatibility promise. Keep the essentials; a completeness pass can wait until the first release.

- Keep current documentation in undated files and update it in place.
- Use `docs/user/` for operator instructions and `docs/developer/` for implementation and contribution guidance.
- Keep one user guide each for reviews, publication, and tasks. Link between topics instead of repeating their explanations.
- Keep domain vocabulary in `CONTEXT.md` and repository working rules in `AGENTS.md`.
- Keep `docs/README.md` as the audience index. Historical plans, reviews, validation reports, and ideas belong on the `docs` archive branch, not in the main checkout. They are not routine implementation context.
- Architecture reviews start with the previous dated report on `docs`, verify progress against current implementation, update `docs/developer/architecture.md` on `main`, and add a new dated report under `docs/arch-review/` in the `docs` worktree. Keep findings and task plans in the report, separate from the maintained architecture reference.
- Writing that report is part of a requested architecture review. Other archive changes require explicit approval. Do not switch the operator's checkout; use separate worktrees. Commits and pushes still require explicit approval.
- Do not rewrite `docs/developer/architecture.md` as part of general documentation cleanup. Check current code when using it for implementation decisions.
- Check commands and configuration against code. Update relative links when moving a guide.
