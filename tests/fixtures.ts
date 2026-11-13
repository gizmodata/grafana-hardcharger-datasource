import { expect, test as base } from '@grafana/plugin-e2e';
import type { Locator, Page } from '@playwright/test';

import type { HardchargerOptions, HardchargerSecureJsonData } from '../src/types';

export { expect };
export type { HardchargerOptions, HardchargerSecureJsonData };

/** The data source the stack provisions (provisioning/datasources/datasources.yml). */
export const PROVISIONED = { fileName: 'datasources.yml', name: 'Hardcharger' };

/**
 * Browser errors no test may cause: an uncaught exception, a console error, or a 5xx answer. A test
 * that provokes one on purpose names it in `expectedErrors`, with the reason next to it.
 *
 * `knownNoise` is what Grafana itself logs on a fresh stack, whatever plugin is installed. Each entry
 * says where it comes from; nothing of this plugin belongs here.
 */
const knownNoise: RegExp[] = [
  // The Grafana Enterprise image ships the grafana-assistant-app plugin, which is not configured in
  // a test stack: its feature-flag request answers 503 and it logs that.
  /grafana-assistant-app/,
  // The data source's settings page asks grafana.com's catalog about the plugin (through Grafana's
  // /api/gnet proxy), which answers 404 for a plugin that is not in the catalog.
  /status of 404 .*\/api\/gnet\/plugins\//,
  // Grafana keeps per-user state (page history, saved queries) in its user storage, and reads a key
  // that was never written as a 404.
  /status of 404 .*\/apis\/userstorage\.grafana\.app\//,
  // The development image (.config/Dockerfile) adds a live-reload script, which only answers while
  // `npm run dev` runs.
  /ERR_CONNECTION_REFUSED .*:35729\/livereload\.js/,
];

interface Fixtures {
  /** Patterns of the console errors and failed responses this test provokes on purpose. */
  expectedErrors: RegExp[];
  browserErrors: void;
}

export const test = base.extend<Fixtures>({
  expectedErrors: [[], { option: true }],
  browserErrors: [
    async ({ page, expectedErrors }, use) => {
      const problems: string[] = [];
      const record = (problem: string) => {
        if (![...knownNoise, ...expectedErrors].some((pattern) => pattern.test(problem))) {
          problems.push(problem);
        }
      };
      page.on('pageerror', (error) => record('uncaught: ' + error.message));
      page.on('console', (message) => {
        if (message.type() === 'error') {
          record('console: ' + message.text() + ' (' + message.location().url + ')');
        }
      });
      page.on('response', (response) => {
        if (response.status() >= 500) {
          record('response: ' + response.status() + ' ' + response.request().method() + ' ' + response.url());
        }
      });
      await use();
      expect(problems, 'the browser reported errors').toEqual([]);
    },
    { auto: true },
  ],
});

/**
 * Types SQL into the plugin's code editor inside `scope` and commits it with Ctrl/Cmd+S, which runs
 * the query (as leaving the editor does).
 */
export async function setSql(page: Page, scope: Locator, sql: string) {
  const editor = scope.getByTestId('hardcharger-sql-editor');
  await expect(editor.locator('.monaco-editor').first()).toBeVisible();
  await editor.locator('.view-lines').first().click();
  // Control, on every host: the test browser presents itself as Chrome on Windows, and the editor
  // takes its shortcuts from that.
  await page.keyboard.press('Control+A');
  await page.keyboard.press('Backspace');
  await page.keyboard.insertText(sql);
  await page.keyboard.press('Control+S');
}
