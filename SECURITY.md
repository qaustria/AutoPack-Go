# Security

## Reporting a vulnerability

Please report security issues through a [private GitHub security advisory](https://github.com/qaustria/AutoPack-Go/security/advisories/new). Do not include API keys, webhooks, passwords, or exploit details in a public issue.

## Credential handling

- The public web interface sends Roblox credentials only with the active conversion request.
- Cone does not write request credentials to server storage or logs.
- “Remember on this device” uses the browser's local storage and can be turned off.
- Self-hosted CLI credentials belong in the ignored `.env.go` file or environment variables.
- Always deploy Cone behind HTTPS before accepting credentials over a network.

Rotate a credential immediately if it is accidentally committed, pasted into an issue, or exposed in logs.
