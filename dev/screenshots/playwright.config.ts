import { defineConfig } from '@playwright/test';
import type { PluginOptions } from '@grafana/plugin-e2e';

import baseConfig from '../../.config/playwright.config';

// The screenshot run: the same Grafana and sign-in as the end-to-end tests, another directory.
export default defineConfig<PluginOptions>(baseConfig, {
  testDir: '.',
  reporter: 'list',
  workers: 1,
});
