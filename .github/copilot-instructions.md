# GitHub Copilot — Repository instruction file

Purpose

This file documents repository-specific guidance you can paste into GitHub Copilot / Copilot Chat "Custom Instructions" or use as a reference when asking Copilot for code. It helps Copilot (and human contributors) produce suggestions that match project style, constraints, and expectations.

How to use

- Repository maintainers: keep this file up to date with the project's style and priorities.
- Contributors: copy relevant parts into your Copilot custom instructions (in VS Code, JetBrains, or GitHub Copilot web settings) or prepend them to prompts when using Copilot Chat.

Where to paste (examples)

- VS Code Copilot extension -> Open Copilot settings -> "Edit Custom Instructions" and paste the two fields below.
- GitHub Copilot web/chat: when starting a session, paste the "System / Project instruction" into the chat or set it as your custom instruction.

Recommended two-field custom instructions

1) "What would you like Copilot to know about you or this project?"

Example:

"This project (MailMind) is an email organization and automation application. Backend components use Python 3.11+ with type hints and pytest for tests. Use `.venv` for the virtual environment. Follow the repository's Makefile targets for install and test. Keep functions small, documented, and typed. Prefer idiomatic Python (PEP 8) and Black formatting. For API endpoints, follow RESTful naming and return JSON with clear error messages."

2) "How would you like Copilot to respond?"

Example:

"Be concise and focused. Provide code that is ready to run and includes minimal tests. Add docstrings in NumPy style and type annotations. When suggesting large changes, include a short rationale and a minimal example usage. Prefer safe, well-tested standard-library solutions; if using third-party packages, mention why and add the pip requirement (package==version)."

Project-specific guidance (suggested contents)

- Python version: 3.11+
- Virtualenv: `.venv`
- Formatting: Black + isort (project should enforce with pre-commit where possible)
- Linting: flake8 / mypy (enable type-checking for public interfaces)
- Testing: pytest; create tests under `tests/`
- Dependency management: prefer `uv` if available; `make install` will fall back to `pip install -r requirements.txt`
- Entry points: document the main run command in `Makefile` under the `run` target
- Secrets: do NOT hardcode secrets or API keys; use environment variables and add `.env` to `.gitignore`
- Commit style: short imperative subject line, but adapt to your project's conventions

Example "system" instruction for Copilot Chat (single block you can paste)

"You are a coding assistant for the MailMind project. Produce code following the project's conventions: Python 3.11, type hints, Black-formatted, NumPy-style docstrings, pytest tests, avoid external dependencies unless necessary, and add minimal test cases for new behavior. When you change APIs, also update README and include migration notes." 

Tips for better suggestions

- Provide a short context paragraph at the top of your prompt describing the file/project responsibilities.
- Show an example of desired output or an existing function to match style.
- Ask Copilot to produce tests and to run static checks mentally (e.g., "Also show potential edge cases and small pytest tests").

Keeping this file useful

- Update this file when project conventions change (formatter, Python version, dependency manager).
- Keep examples small and focused — Copilot responds best to concrete instructions.

Optional: repository-enforced configuration

Even with Copilot instructions, it's best to enforce style automatically:

- Add `.editorconfig`, `pyproject.toml` with Black/isort settings, `mypy.ini` or `pyproject` for mypy config.
- Add GitHub Actions or other CI to run linters, mypy, and tests on PRs.
- Add `pre-commit` to enforce formatting before commits.

If you want, I can:
- Create this file in the repo (done), and also add a small `pyproject.toml` with Black/isort, `requirements.txt` placeholder, and a `pre-commit` config. Tell me which of these you want next.

---

Last updated: 2025-11-12
