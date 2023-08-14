__webpack_public_path__ = "/docs/"

module.exports = {
  title: 'jsPolicy Docs | JavaScript-Based Policies for Kubernetes',
  tagline: 'JavaScript-Based Policies for Kubernetes',
  url: 'https://jspolicy.com',
  baseUrl: __webpack_public_path__,
  favicon: '/media/jspolicy-favicon.png',
  organizationName: 'loft-sh', // Usually your GitHub org/user name.
  projectName: 'jspolicy', // Usually your repo name.
  themeConfig: {
    colorMode: {
      disableSwitch: true,
    },
    navbar: {
      logo: {
        alt: 'jspolicy',
        src: '/media/jspolicy-logo-dark.svg',
        href: 'https://jspolicy.com/',
        target: '_self',
      },
      items: [
        {
          href: 'https://jspolicy.com/',
          label: 'Website',
          position: 'left',
          target: '_self'
        },
        {
          to: '/docs/why-jspolicy',
          label: 'Docs',
          position: 'left'
        },
        {
          href: 'https://loft.sh/blog',
          label: 'Blog',
          position: 'left',
          target: '_self'
        },
        {
          href: 'https://slack.loft.sh/',
          className: 'slack-link',
          'aria-label': 'Slack',
          position: 'right',
        },
        {
          href: 'https://github.com/loft-sh/jspolicy',
          className: 'github-link',
          'aria-label': 'GitHub',
          position: 'right',
        },
      ],
    },
    algolia: {
      apiKey: "3bfe37ad9d4f78fd64329ace5b3dc3c6",
      indexName: "jspolicy",
      placeholder: "Search...",
      algoliaOptions: {}
    },
    footer: {
      style: 'light',
      links: [],
      copyright: `Copyright © ${new Date().getFullYear()} <a href="https://loft.sh/">Loft Labs, Inc.</a>`,
    },
  },
  presets: [
    [
      '@docusaurus/preset-classic',
      {
        docs: {
          path: 'pages',
          routeBasePath: '/',
          sidebarPath: require.resolve('./sidebars.js'),
          editUrl:
            'https://github.com/loft-sh/jspolicy/edit/main/docs/',
        },
        theme: {
          customCss: require.resolve('./src/css/custom.css'),
        },
      },
    ],
  ],
  plugins: [],
  scripts: [
    {
      src:
        'https://cdnjs.cloudflare.com/ajax/libs/clipboard.js/2.0.0/clipboard.min.js',
      async: true,
    },
    {
      src:
        '/docs/js/custom.js',
      async: true,
    },
  ],
};
