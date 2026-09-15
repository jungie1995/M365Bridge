# M365 Bridge launcher

This repository contains safe launcher scripts for a local M365 Bridge installation.

Install the bridge under `C:\m365bridge`, connect the Microsoft account locally, then double-click:

- `start-bridges.cmd` starts the text API on `127.0.0.1:8000` and image API on `127.0.0.1:8001`.
- `stop-bridges.cmd` stops both bridge processes.

The launcher uses isolated runtime folders so the two services do not contend for the same log file. The existing local token directory is linked, not copied. Credentials, logs, binaries, generated media, and runtime data are excluded from Git.
