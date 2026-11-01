# Setup and Microsoft sign-in

Use this guide for a fresh installation, device-code login, browser login, and
reconnecting an existing OpenCode or other API client.

**Sign in from the installation folder that runs your API.** Credentials are
stored under `data/` in the command's working directory. Signing in from a
different source checkout does not reconnect an already-running installation.
The gateway API key is a separate credential that your local clients use.

## Choose a login method

- **Browser sign-in:** use `connect-microsoft.cmd` in an installed copy. It
  completes Microsoft text sign-in and the separate Designer image authorization,
  then starts both local services. This is the recommended Windows path when you
  need image generation or the web interface's browser-backed account features.
- **Device code:** use `login.cmd`, or `m365-bridge.exe login`. The terminal
  displays a Microsoft sign-in URL and a one-time code. You can authorize on this
  computer or another device. This is an optional text-login path; the bridge
  does not automate a local browser for this flow.
- **Terminal-assisted PKCE:** use `m365-bridge.exe login -pkce` if you deliberately
  want to open the printed authorization URL and paste the resulting callback
  into your own terminal.

Microsoft can require MFA or consent and can reject a flow under organization
policy. Device code does not bypass those requirements or grant additional model
access. The [Microsoft device-code documentation](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-device-code)
describes the authorization process.

## Install on Windows

Download this repository's source ZIP and extract it, or clone it with Git:

```powershell
git clone https://github.com/jungie1995/M365Bridge.git
Set-Location .\M365Bridge
```

For guided browser sign-in, double-click `setup-bridge.cmd`. The default install
folder is `%LOCALAPPDATA%\M365Bridge`. Setup can install missing Git, Go and Edge
dependencies through Windows Package Manager. Use the Go version required by
`go.mod`, or allow Go's supported automatic toolchain download.

To install first and choose device-code login afterward, run this from the source
folder instead:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\setup-bridge.ps1 -NoLogin
Set-Location "$env:LOCALAPPDATA\M365Bridge"
```

The installer creates a private random gateway API key in `data/.env`, installs
matching executables and the login/start shortcuts, and leaves account setup for
your chosen method. An upgrade preserves existing settings and account files.
Run upgrades when your clients and bridge workers are idle.

For an existing installation at `C:\m365bridge`, specify that location explicitly:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\setup-bridge.ps1 -InstallRoot C:\m365bridge -NoLogin
Set-Location C:\m365bridge
```

Do not create a second account folder accidentally when your clients still point
at the first one. For a different Microsoft account, choose a separate installation
and unused ports deliberately.

## Device-code login

From the **installed** folder:

```powershell
.\login.cmd
```

The equivalent command is:

```powershell
.\m365-bridge.exe login
```

1. Keep the terminal open. It displays a Microsoft verification URL and a code.
2. Open the displayed URL on your computer or phone. Enter the code from this
   command, choose the intended Microsoft account, and complete any Microsoft
   prompts. Do not use a code supplied by somebody else.
3. Wait for the terminal to confirm that text credentials were saved. If the code
   expires or authorization is declined, run the command again for a new code.
4. Start the text service:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\start-bridge.ps1 -Mode text
```

Device-code and PKCE login update the saved tenant, account and OAuth client ID
while preserving the existing gateway key, web-interface password, tool settings,
comments and other `data/.env` entries. Login itself does not start the API.

If you switch between native/device-code and browser sign-in, stop workers sharing
this account folder before the switch and restart them afterward. Their OAuth
client ID is loaded when they start and must match the newly saved credentials.
An older running binary does not gain the new login code until it is upgraded.

Text login is not an image-generation check. The command attempts Designer token
acquisition, but a successful text login does not establish that Designer can
renew. If images need authorization, complete the browser flow below.

## Browser login and reconnect

In an installed copy, run:

```powershell
.\connect-microsoft.cmd
```

Select the intended Microsoft account in the dedicated Edge window and complete
sign-in/MFA yourself. Leave that window open until the helper finishes. If the
saved Designer credential cannot renew, the helper submits one disclosed apple
image setup prompt to complete Designer authorization. This can take longer than
the text sign-in and can appear in Copilot history.

The installed reconnect shortcut delegates to `scripts/connect-microsoft.ps1`.
After successful authorization it starts missing text and image workers using
the installation's configured ports. It preserves the gateway key and refuses a
different account during browser reconnect.

For browser sign-in without starting services, use:

```powershell
.\login-browser.cmd
```

Start existing credentials later with `start-bridges.cmd`. Do not repeat login
just because a local process is stopped.

See [Windows and Designer setup](docs/WINDOWS_SETUP.md) for the detailed browser
authorization behavior and Content Studio integration.

## Advanced PKCE login

From the installed folder:

```powershell
.\m365-bridge.exe login -pkce
```

Open the authorization URL printed by the command, complete Microsoft sign-in,
and paste the final callback URL or code into that same terminal. Keep the
callback private; it is an authorization credential. Successful native PKCE login
uses the same setting-preserving save path as device code. Restart workers after
changing login methods.

## Running directly from a source checkout

The source folder and installed folder are different account locations. If you
deliberately want a standalone source copy, build it with
`build-m365-bridge.cmd`, then use its `login.cmd` or `login-browser.cmd`. Those
shortcuts save credentials in that source copy's `data/` directory.

Before starting a source-only server, configure a private `M365_API_KEY` in its
`data/.env`; the bare `go build` path does not run the installer that creates one.
`start-bridge.cmd` starts the standalone text server on port 8000. The source-only
`connect-microsoft.cmd` performs browser sign-in and starts that text server.
The installed version detects its browser-login executable and uses the managed
dual-service reconnect path instead.

## Verify the connection

The default text endpoint is `http://127.0.0.1:8000/v1`; the image worker uses
`http://127.0.0.1:8001/v1`. A different image port alone does not enable image
routing. The supplied starter sets the image worker's routing flag separately.
Custom installation ports are recorded in `install-manifest.json`.

A reachability check does not call Microsoft:

```powershell
Invoke-RestMethod http://127.0.0.1:8000/v1/health
```

For a real text check, use the private gateway key from your own `data/.env` in
the `Authorization: Bearer ...` header and send a small request to
`/v1/chat/completions`, for example model `gpt-5.6-reasoning` with the prompt
`Reply only with OK`. Do not paste the key into an issue, chat, screenshot or
committed client configuration. An HTTP 200 with the expected answer verifies
that request; `/health` and `/v1/models` alone do not verify upstream access.

In OpenCode, point the provider at the configured text endpoint and use the same
gateway key. Keep your existing model IDs and local provider key when reconnecting
Microsoft. Close and reopen the relevant clients/workers after changing the
account's OAuth client or installation folder. A listed selector or a model's
self-description does not establish the underlying vendor/version or your
account's entitlement.

## When something fails

- **No local response:** start the service in the intended installation and check
  its configured port. Login does not fix a stopped process or a port owned by
  another application.
- **Gateway key rejected:** check the client's local key and endpoint against
  that installation's `data/.env`; this is separate from Microsoft sign-in.
- **Microsoft authentication failed:** reconnect from the same account folder
  used by the API and restart workers after changing the login method.
- **Device code blocked or expired:** use a new command/code, or the allowed
  browser flow if your organization's policy permits it. Do not try to bypass
  consent, MFA or organization restrictions.
- **Text login succeeded but images wait/fail:** complete Designer authorization
  with browser reconnect and test an actual image request. The local readiness
  response reports saved credential state, not image-generation success.
- **A model or route returns 403/502 after login:** inspect its error code and
  account/route availability. A valid sign-in does not prove every selector works
  or repair the separate experimental Outlook route.

Native-client refresh tokens generally have a longer default lifetime than SPA
tokens, but Microsoft can expire or revoke them. Neither login method guarantees
permanent unattended access. See Microsoft's [refresh-token guidance](https://learn.microsoft.com/en-us/entra/identity-platform/refresh-tokens)
and [Conditional Access flow controls](https://learn.microsoft.com/en-us/entra/identity/conditional-access/concept-authentication-flows).

Keep `data/`, token caches, cookies and the dedicated browser profile private.
They are not part of a source backup or a GitHub push. This repository is an
unofficial compatibility bridge, not a Microsoft-supported Copilot API.
