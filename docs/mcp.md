# Connect an assistant with MCP

Hosted Drop offers a remote Model Context Protocol (MCP) endpoint at `https://HOST/mcp`, with OAuth (automatic client registration, S256 PKCE, endpoint-bound tokens). It is part of hosted mode ([Hosting](hosting.md)): `serve --hosted` requires `--mcp`. The MCP and OAuth implementation comes from pocketcontext-core.

## Tools

Drop's tools are not there yet: the endpoint authenticates, answers the protocol and lists no tools. A later release adds exactly two, `drop_read` and `drop_write`, which call Drop's operations ([Operations](ops.md), `POST /api/drop/op`) with the caller's own account, so every call is checked against that account's role and ownership on the server; `drop_write`'s op enum and table come from the registry in `internal/dropapi`. No tool runs shell commands, reads host files or administers the server. A new tool needs the owner's approval.

## Connect and consent

1. Sign in to the hosted reader in the browser profile that will approve the connection.
2. Copy the address from the reader's Connect page and add it as a custom MCP server in the assistant's connector settings. Leave manual client credentials empty.
3. The assistant opens Drop's consent page. If you are not signed in there, Log in returns you to it after the email sign-in. Approve the request.

A grant belongs to the account that approved it and gets only what that account may do. Each account may hold 10 connections; accounts other than the owner stop short of the instance limits, so the owner can always connect.

## Native desktop clients

Native clients can register HTTP callbacks on literal `127.0.0.1`, `[::1]` or exactly `localhost` with their chosen port, and private schemes based on a reversed domain, such as `com.example.app:/oauth/callback`. Other public callbacks must use HTTPS. For an intended client that needs a legacy native scheme, set `DROP_MCP_REDIRECT_EXCEPTIONS` to a JSON array of exact callback URIs in the private service configuration and restart. Malformed values fail startup.

## Operate and revoke

OAuth state is machine-local in the private cache, separate from the database. The reader's Connect page lists the account's connected apps and disconnects one (its tokens stop working at once); a browser visit to `/oauth/connections` leads there.
