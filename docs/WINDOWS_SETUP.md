# Windows: browser sign-in and Content Studio setup

## Easiest installation

An extracted GitHub source ZIP also works with setup-bridge.cmd. Without Git metadata the install manifest reports `source-archive` rather than inventing a commit ID; download updated source to upgrade that copy.

For Content Studio V1, use **install-with-m365.cmd** in the Content Studio repository. It installs dependencies, builds this bridge fork, guides Microsoft sign-in, starts both services and configures Content Studio. Your interactive Codex account is not rewritten. Both repositories must include this setup update.

For a bridge-only installation, clone this repository and double-click **setup-bridge.cmd**. If Windows Package Manager is missing, install Microsoft's App Installer first. Windows may request approval for dependency installation. The installer requires Git, Go (the version in go.mod or its supported automatic toolchain download), internet access and Microsoft Edge. It uses Windows PowerShell 5.1 or later. No paid OpenAI API key is required.

1. Setup creates `%LOCALAPPDATA%\M365Bridge` with matching text, image and browser-login binaries.
2. A dedicated Edge window opens. Select your work/school Microsoft account and complete Microsoft authentication/MFA yourself.
3. The bridge redeems its own authorization code with PKCE/state protection and derives account IDs from the Microsoft response. It saves the renewable credentials locally and preserves the installation's random gateway key.
4. Both services start. Existing signed-in installations can use **start-bridges.cmd** from the install folder. **connect-microsoft.cmd** handles both first login and later re-login and starts any missing services.

The Microsoft organization may require admin consent or block this application. Browser login cannot bypass those policies, account licensing, quotas or service outages. This is an unofficial compatibility bridge, not a Microsoft-supported Copilot API. A local configuration check cannot promise that an upstream generation will succeed.

## Text and images are separate routes

| Service | Default client URL | Purpose |
| --- | --- | --- |
| Text bridge | `http://127.0.0.1:8000/v1` | Codex coding/tool calls, idea and script generation |
| Image bridge | `http://127.0.0.1:8001/v1` | Topic illustrations through Microsoft image services |

The image process must start with `M365_BROWSER_IMAGE_ROUTING=1`; a different port alone does not enable image mode. The supplied starter sets it only for the image process. Both processes use the same install folder and account files. Do not start old `text-runtime`/`image-runtime` copies alongside these launchers.

### Image broker on a new computer

Text authentication and image-download authentication are different. The image path uses a separate Microsoft Designer broker token (`data/tokens/rt_broker.txt`) and Designer access-token cache. Do **not** copy either from another PC or put them in `.env`. Browser sign-in saves this installation's Microsoft session; when the image service first needs Designer access, it automatically acquires its broker credential from that session. An expired broker credential is reacquired through the saved session. If Microsoft requires interaction again, use **Connect Microsoft account** on this PC.

The local readiness check verifies routing and saved sign-in configuration, not successful image generation. Test an actual small image request after sign-in before relying on unattended illustration jobs. Missing permissions, expired Microsoft sessions, licensing restrictions or upstream errors must be reported as failures, never as a successful image connection. Automated tests cover missing and expired broker credentials, but they do not certify a different account's Microsoft entitlement.

Custom ports can be passed to `scripts/install-bridge.ps1 -TextPort 18000 -ImagePort 18001`. They are recorded in `install-manifest.json` and reused by the starter and Content Studio configuration. The starter leaves already-running managed services alone and refuses to kill unrelated processes occupying a port.

## Connect Content Studio after a separate bridge installation

In Content Studio **Settings > Local execution workers**:

1. Click **Configure local bridge**. The default folder is detected; enter a custom install folder only if needed.
2. Click **Connect Microsoft account** for first login or expired sign-in.
3. Click **Check text and image connections**. A wrong image mode, rejected local key, old build or missing sign-in is reported separately.
4. Select **M365Bridge** as the worker provider and save. The combined installer already selects it for the initial brand; this UI action does not silently replace an existing OpenAI worker choice.

`GET /v1/bridge/readiness` is gateway-key protected and reports configuration only. It does not expose IDs, keys, tokens, cookies or signed media URLs, and does not perform inference. `upstream_verified: false` is intentional.

## Restart, upgrade and privacy

- Use `scripts/setup-bridge.ps1 -RegisterAutoStart` to register startup after Windows sign-in. MFA/browser login is never launched unattended by that task.
- Update the source with `git pull --ff-only`, then rerun setup when jobs are idle. The installer backs up binaries, records hashes and preserves private data. `-NoLogin` defers interactive sign-in; `install-bridge.ps1 -NoStart` leaves installed services stopped.
- Existing installs in `C:\m365bridge` remain supported; use that explicit `-InstallRoot` when upgrading one.
- Managed launchers load identity and gateway keys from their own private `data/.env`, ignoring stale inherited account/key variables. Direct advanced CLI launches retain environment-variable overrides. Standard single/double-quoted `.env` values are accepted.
- Reconnect rejects another account; it does not silently replace the configured account. For a different account, install into a separate folder with unused ports and configure Content Studio to use that installation deliberately.
- Keep `data/` and the dedicated Edge profile private. Passwords/MFA stay in Microsoft’s window. The bridge stores local credentials/session material for renewal; never commit or share it, copy it between PCs, or paste it into chat.
- The original export-based `setup-wizard` remains an advanced/manual path, not the recommended Windows first-login path.

Microsoft's [authorization-code/PKCE documentation](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow) explains the account selector and authorization flow. It does not certify this unofficial bridge.

## Tested boundaries

Regression tests cover first-account discovery, wrong-account rejection before credential replacement, PKCE/state checks, missing session evidence, safe save failures, and distinct image-mode readiness. The isolated Windows installer test covers a fresh install, upgrade, paths with spaces, private-setting preservation and no unintended service startup. A real first-time account/MFA completion on a separate PC still requires the user to complete Microsoft sign-in; mocks do not prove account entitlement.
