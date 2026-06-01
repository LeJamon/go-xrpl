import { defineConfig } from 'vitepress';

export default defineConfig({
  title: 'goXRPL',
  description: 'A native Go implementation of an XRP Ledger node',

  lang: 'en-US',
  base: '/go-xrpl/',

  // The prose docs predate this site and link out to repo files
  // (../CONTRIBUTING.md, ../justfile, source paths). Those targets live
  // outside the VitePress srcDir, so dead-link checking would fail the build.
  ignoreDeadLinks: true,

  // Built only from the maintained prose docs. The home page is index.md, so
  // the top-level README.md is dropped; archived snapshots, the unrelated
  // superpowers/ tree, and the replay-lab doc (owned by xrpl-state-compare)
  // are excluded too.
  srcExclude: ['README.md', 'archive/**', 'superpowers/**', 'mainnet-replay-architecture.md'],

  // Serve the ADR README as the /adr/ directory index.
  rewrites: {
    'adr/README.md': 'adr/index.md',
  },

  head: [
    ['link', { rel: 'icon', type: 'image/png', href: '/go-xrpl/favicon.png' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
    [
      'link',
      {
        href: 'https://fonts.googleapis.com/css2?family=Unbounded:wght@400;500;600;700;800&family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap',
        rel: 'stylesheet',
      },
    ],
  ],

  themeConfig: {
    logo: '/commons_ligth_logo.png',

    nav: [
      {
        text: 'Documentation',
        items: [
          { text: 'Introduction', link: '/' },
          { text: 'Architecture', link: '/architecture' },
          { text: 'Operating a node', link: '/operating' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Supported transactions', link: '/supported-transactions' },
          { text: 'RPC methods', link: '/rpc-methods' },
          { text: 'Amendments', link: '/amendments' },
          { text: 'pkg.go.dev', link: 'https://pkg.go.dev/github.com/LeJamon/go-xrpl' },
        ],
      },
      {
        text: 'Links',
        items: [
          { text: 'GitHub', link: 'https://github.com/LeJamon/go-xrpl' },
          {
            text: 'Contributing',
            link: 'https://github.com/LeJamon/go-xrpl/blob/main/CONTRIBUTING.md',
          },
          { text: 'XRPL Commons', link: 'https://www.xrpl-commons.org' },
        ],
      },
    ],

    sidebar: [
      {
        text: 'Overview',
        items: [
          { text: 'Introduction', link: '/' },
          { text: 'Architecture', link: '/architecture' },
          { text: 'Operating a node', link: '/operating' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Supported transactions', link: '/supported-transactions' },
          { text: 'RPC methods', link: '/rpc-methods' },
          { text: 'Amendments', link: '/amendments' },
        ],
      },
      {
        text: 'Conformance',
        items: [
          { text: 'Conformance', link: '/conformance' },
          { text: 'Conformance status', link: '/conformance-status' },
        ],
      },
      {
        text: 'Architecture Decision Records',
        items: [
          { text: 'Index', link: '/adr/' },
          { text: '0001 — rippled as the specification', link: '/adr/0001-rippled-as-specification' },
          { text: '0002 — Native Go, not a port', link: '/adr/0002-native-go-not-a-port' },
          { text: '0003 — Single-writer engine', link: '/adr/0003-single-writer-engine' },
          { text: '0004 — Storage architecture', link: '/adr/0004-storage-architecture' },
          { text: '0005 — CGO for crypto and TLS', link: '/adr/0005-cgo-for-crypto-and-tls' },
        ],
      },
    ],

    socialLinks: [{ icon: 'github', link: 'https://github.com/LeJamon/go-xrpl' }],

    footer: {
      message: 'Released under the ISC License.',
      copyright: 'Copyright © 2024–2026 Thomas Hussenet',
    },

    search: {
      provider: 'local',
    },
  },

  markdown: {
    lineNumbers: true,
  },
});
