/**
 * Copyright (c) 2017-present, Facebook, Inc.
 *
 * This source code is licensed under the MIT license found in the
 * LICENSE file in the root directory of this source tree.
 */

module.exports = {
  adminSidebar: [
    {
      type: 'doc',
      id: 'why-jspolicy',
    },
    {
      type: 'category',
      label: 'Getting Started',
      collapsed: false,
      items: [
        {
          type: 'doc',
          id: 'quickstart',
        },
/*        {
          type: 'doc',
          id: 'examples',
        },*/
        {
          type: 'category',
          label: 'Full Guide',
          collapsed: false,
          items: [
            'getting-started/installation',
            'getting-started/understand-jspolicy',
            'getting-started/work-with-policies',
            'getting-started/cleanup',
          ],
        },
      ],
    },
    {
      type: 'category',
      label: 'Apply Policies',
      collapsed: false,
      items: [
        'using-policies/basics',
        'using-policies/policy-types',
//        'using-policies/policy-catalog',
      ],
    },
    {
      type: 'category',
      label: 'Write Policies',
      collapsed: false,
      items: [
        'writing-policies/workflow',
        'writing-policies/policy-sdk',
        'writing-policies/testing-policies',
        'writing-policies/publishing-policies',
      ],
    },
    {
      type: 'category',
      label: 'JavaScript Reference',
      collapsed: false,
      items: [
        'reference/functions',
        'reference/request-context',
      ],
    },
    {
      type: 'category',
      label: 'CRD Reference',
      collapsed: false,
      items: [
        'reference/policy-crd',
        'reference/policybundle-crd',
        'reference/policyviolations-crd',
      ],
    },
/*    {
      type: 'category',
      label: 'Operator Guide',
      collapsed: false,
      items: [
        'operators-guide/best-practices',
        'operators-guide/violation-handling',
        'operators-guide/high-availability',
        'operators-guide/monitoring',
        'operators-guide/upgrades',
      ],
    },*/
    {
      type: 'doc',
      id: 'architecture',
    },
    {
      type: 'link',
      label: 'Originally created by Loft',
      href: 'https://loft.sh/',
    },
  ],
};
