import * as path from 'node:path';

import { expect, test } from '@grafana/plugin-e2e';

// Captures the screenshots of the README and the plugin catalog (src/img/screenshot-*.png) from the
// development stack: `docker compose up`, then `npm run screenshots`.

const out = (name: string) => path.join(__dirname, '..', '..', 'src', 'img', name);

test.use({ viewport: { width: 1280, height: 900 }, colorScheme: 'dark' });

test('configuration', async ({ gotoDataSourceConfigPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml', name: 'Hardcharger' });
  const configPage = await gotoDataSourceConfigPage(ds.uid);
  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', { hasText: 'Connected to Hardcharger' });
  await page.getByText('Connect with').scrollIntoViewIfNeeded();
  await page.mouse.move(0, 0);
  await page.screenshot({ path: out('screenshot-config.png'), fullPage: true });
});

test('query editor', async ({ gotoPanelEditPage, readProvisionedDashboard, page }) => {
  const dashboard = await readProvisionedDashboard({ fileName: 'hardcharger-overview.json' });
  const panelEditPage = await gotoPanelEditPage({ dashboard: { uid: dashboard.uid }, id: '1' });
  await expect(panelEditPage.getQueryEditorRow('A').getByTestId('hardcharger-sql-editor')).toBeVisible();
  await expect(panelEditPage.panel.getErrorIcon()).toHaveCount(0);
  await expect(page.locator('.monaco-editor .view-line').first()).toContainText('SELECT');
  await page.waitForTimeout(1500); // the chart's first paint
  await page.mouse.move(0, 0);
  await page.screenshot({ path: out('screenshot-query.png') });
});

test('dashboard', async ({ gotoDashboardPage, readProvisionedDashboard, page }) => {
  const dashboard = await readProvisionedDashboard({ fileName: 'hardcharger-overview.json' });
  const dashboardPage = await gotoDashboardPage({ uid: dashboard.uid });
  for (const title of ['CPU by host', 'Requests by region', 'Latest reading per host']) {
    await expect(dashboardPage.getPanelByTitle(title).getErrorIcon()).toHaveCount(0);
  }
  await expect(dashboardPage.getPanelByTitle('Latest reading per host').data).toContainText(['web-1']);
  await page.waitForTimeout(1500); // the charts' first paint
  await page.mouse.move(0, 0);
  await page.screenshot({ path: out('screenshot-dashboard.png') });
});
