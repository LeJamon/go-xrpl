---
layout: home

hero:
  name: goXRPL
  text: A native Go XRP Ledger node
  tagline: An independent, from-scratch Go implementation of an XRPL node — validated against rippled as the specification.
  image:
    src: /commons_ligth_logo.png
    alt: XRPL Commons
  actions:
    - theme: brand
      text: Architecture
      link: /architecture
    - theme: alt
      text: Operating a node
      link: /operating
    - theme: alt
      text: GitHub
      link: https://github.com/LeJamon/go-xrpl

features:
  - title: Native Go, not a port
    details: A clean-room Go implementation that follows XRPL semantics while staying idiomatic Go — see the Architecture Decision Records for the load-bearing choices.
    link: /adr/
  - title: Conformance to rippled
    details: rippled is the source of truth. Behaviour is verified against it through a dedicated conformance suite.
    link: /conformance
  - title: Protocol reference
    details: Supported transaction types, RPC methods, and amendments — generated from the registries so they never go stale.
    link: /supported-transactions
  - title: Run a node
    details: Build requirements (including CGO), running the server, and the full xrpld.toml configuration reference.
    link: /operating
---
