# Designer browser authorization repair - 2026-09-16

## Cause and fix

A successful text login did not complete the separate Designer broker authorization. The HTTP-only broker fallback encountered Microsoft's JavaScript/error page, including immediately after a fresh text login. Download failures could then be returned as HTTP 200 with an inaccessible signed URL instead of image bytes.

The Windows login helper now checks existing Designer renewal directly with Microsoft. If it cannot renew, the dedicated Edge window completes the real Copilot image flow using a disclosed small setup prompt. Only the expected HTTPS Designer token exchange in that tab is observed. Client, scope, tenant/account identity, size and expiry checks precede persistence. No browser-history export or manual token transfer is used. Text-only completion is no longer an overall connected result.

Image requests check authorization before spending an image turn. A failed download returns an error, not a raw authenticated URL. PNG/JPEG/GIF bytes are decoded with size/dimension bounds; HTML and truncated images fail. Image downloads do not follow redirects carrying credentials. Broker cookies stay on the exact Microsoft login host.

## Verification

- Complete native Windows `go test ./...` passed. Three filesystem tests require the normal Windows account because sandbox handle canonicalization is denied; the normal-account run passed without skipping them.
- Regressions cover fresh Designer storage, encrypted broker renewal storage, wrong account/resource/client, ambiguous scope, cancellation, invalid/oversized responses, own-tab isolation, redirects, one-time response consumption and reconnect renewal failure despite a cached access token.
- Image tests cover failed downloads in both response formats, HTML masquerading as PNG, truncated bytes, valid PNG decoding and failure before generation when authorization is unavailable.
- Isolated fresh/upgrade installation and startup tests passed: custom ports, paths with spaces, missing legacy gateway-key repair, credential preservation, matching binary aliases and no changes to unrelated services.
- Live dedicated-browser authorization completed and saved new Designer credentials for the configured account without manual imports.
- A live image request returned HTTP 200 with a decodable **1254 x 1254 PNG**, **510,389 bytes**, in **50.5 seconds**. The blue-mug image was visually inspected. This was real byte evidence, not a health response or an inaccessible URL.

## Limits

This is an unofficial bridge. Microsoft may require MFA/consent or renewed browser sign-in, reject account entitlement, exhaust a quota or change its web flow. Installation cannot guarantee perpetual login or image quality. The isolated new-install tests are not a physical clean-PC/Microsoft-account certification. Credentials and generated images are excluded from Git. No paid OpenAI image API or social-platform publication was used.
