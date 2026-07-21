<h1 align="center">nextendo-nex</h1>

<p align="center">
  <b>A from-scratch implementation of Nintendo's NEX / PRUDP online protocol, written in Go.</b>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/license-PolyForm%20Shield%201.0.0-orange" alt="License: PolyForm Shield 1.0.0">
  <img src="https://img.shields.io/badge/go-1.23%2B-00ADD8" alt="Go 1.23+">
</p>

---

## What is this?

**nextendo-nex** is the server core that powers the [Nextendo Network](https://nextendo.network)
game servers — Mario Kart 8 Deluxe, Splatoon 2, Super Smash Bros. Ultimate, and others.

NEX is the client-server middleware many Nintendo games use for matchmaking, rankings, and other
online services, layered on top of the PRUDP transport. This package reimplements the **server side**
of that stack from scratch:

- **PRUDP** transport (reliable UDP: connection handshake, fragmentation, acknowledgements)
- **RMC** message layer (the request/response format NEX methods speak)
- **Kerberos-style** ticket authentication (auth server ↔ secure server)
- The **common service protocols** games build on (matchmaking, ranking, data store, utility, …)

It has **no third-party NEX dependencies** — only permissive Go libraries (`gorilla/websocket`,
`lxzan/gws`). The module is `github.com/NextendoNetwork/nextendo-nex`.

## Usage

This is a library, not a runnable server on its own. A game server imports it, registers the
protocols and methods that game needs, and starts an endpoint. See the per-game repositories for
concrete servers built on top of this core.

```go
import nex "github.com/NextendoNetwork/nextendo-nex"
```

## What this is not

This project ships **no** Nintendo code, keys, or copyrighted assets. It is an independent,
clean-room reimplementation of a publicly-documented protocol, for use with a community-run
replacement service. It is not affiliated with, endorsed by, or associated with Nintendo.

## Credits

- The NEX / PRUDP protocol is publicly documented by
  [Kinnay's NintendoClients](https://github.com/kinnay/NintendoClients) (MIT); this implementation
  follows that documentation.

## License

Released under the **[PolyForm Shield License 1.0.0](LICENSE.md)** — a source-available license: you
may read, use, modify, and self-host the code, but not use it to provide a product that competes with
Nextendo Network.
